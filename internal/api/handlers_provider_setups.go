package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/proxmoxsetup"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

type providerSetupView struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	Ready     bool      `json:"ready"`
	Name      string    `json:"name,omitempty"`
	Endpoint  string    `json:"endpoint,omitempty"`
	Command   string    `json:"command,omitempty"`
}

func (s *Server) handleCreateProviderSetup(w http.ResponseWriter, r *http.Request) {
	if !s.tailcatAvailable() {
		unprocessable(w, "Private provider setup needs private connections enabled.", []fieldError{{"connection", "Enable private connections in Zoomies before connecting Proxmox."}})
		return
	}
	var req struct {
		ControllerURL string `json:"controller_url"`
	}
	if !decode(w, r, &req) {
		return
	}
	controllerURL := strings.TrimRight(strings.TrimSpace(req.ControllerURL), "/")
	if controllerURL == "" {
		address, err := s.ensureTailcat(r.Context())
		if err != nil {
			unprocessable(w, err.Error(), nil)
			return
		}
		controllerURL = "tailcat://" + address
	} else {
		if msg := checkControllerURL(controllerURL); msg != "" {
			unprocessable(w, msg, []fieldError{{"controller_url", msg}})
			return
		}
		if !strings.HasPrefix(controllerURL, "https://") {
			unprocessable(w, "Use an HTTPS controller address for Proxmox setup.", []fieldError{{"controller_url", "The setup command sends credentials; the controller address must use HTTPS."}})
			return
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		s.internal(w, r, "creating a provider setup token", err)
		return
	}
	token := hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))
	p := &store.ProviderSetup{ID: store.NewID(store.PrefixProviderSetup), TokenHash: hash[:], ExpiresAt: s.ctrl.Store().Now().Add(time.Hour)}
	if err := s.ctrl.Store().CreateProviderSetup(r.Context(), p); err != nil {
		s.internal(w, r, "creating provider setup", err)
		return
	}
	s.auth.Auditor().Created(r.Context(), Identity(r.Context()), "provider_setup", p.ID,
		providerSetupView{ID: p.ID, ExpiresAt: p.ExpiresAt})
	tag := version.Channel(version.Version)
	if pinned, ok := version.InstallTag(version.Version); ok {
		tag = pinned
	}
	writeJSON(w, http.StatusCreated, providerSetupView{ID: p.ID, ExpiresAt: p.ExpiresAt, Command: proxmoxsetup.Command(controllerURL, p.ID, token, tag)})
}

// The host capability binds one connection and acknowledges identical retries.
// It cannot authenticate
// to the user API, enrol a runner, or read back the credential it uploaded.
func (s *Server) handleCompleteProviderSetup(w http.ResponseWriter, r *http.Request) {
	if s.key == nil {
		s.noEncryptionKey(w)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	hash := sha256.Sum256([]byte(token))
	p, err := s.ctrl.Store().GetProviderSetup(r.Context(), chiURLParam(r, "id"))
	if err != nil || token == "" || subtle.ConstantTimeCompare(hash[:], p.TokenHash) != 1 {
		writeError(w, http.StatusUnauthorized, errorEnvelope{Error: errorBody{Code: codeUnauthorized, Message: "This setup command has expired or has already been used."}})
		return
	}
	var conn proxmoxsetup.Connection
	if !decode(w, r, &conn) {
		return
	}
	if err := conn.Validate(); err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	raw, err := json.Marshal(conn)
	if err != nil {
		s.internal(w, r, "encoding provider setup", err)
		return
	}
	// An identical retry acknowledges a lost success response without
	// letting a capability replace the connection it already staged.
	if len(p.PayloadEnc) != 0 {
		previous, err := s.key.Open(p.PayloadEnc)
		if err != nil {
			s.internal(w, r, "opening completed setup", err)
			return
		}
		if !bytes.Equal(previous, raw) {
			writeError(w, http.StatusUnauthorized, errorEnvelope{Error: errorBody{Code: codeUnauthorized, Message: "This setup already contains another connection."}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sealed, err := s.key.Seal(raw)
	if err != nil {
		s.internal(w, r, "sealing provider setup", err)
		return
	}
	if err := s.ctrl.Store().CompleteProviderSetup(r.Context(), p.ID, hash[:], sealed); err != nil {
		s.fail(w, r, "completing provider setup", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setupConnection(w http.ResponseWriter, r *http.Request, id string) (*store.ProviderSetup, *proxmoxsetup.Connection, bool) {
	p, err := s.ctrl.Store().GetProviderSetup(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, r, "reading provider setup", err)
		return nil, nil, false
	}
	if err != nil {
		unprocessable(w, "This setup command has expired. Generate another command and run it on the Proxmox host.", []fieldError{{"setup_id", "Generate a new setup command."}})
		return nil, nil, false
	}
	if len(p.PayloadEnc) == 0 {
		return p, nil, true
	}
	raw, err := s.key.Open(p.PayloadEnc)
	if err != nil {
		s.internal(w, r, "opening provider setup", err)
		return nil, nil, false
	}
	var conn proxmoxsetup.Connection
	if err := json.Unmarshal(raw, &conn); err != nil {
		s.internal(w, r, "reading provider setup", err)
		return nil, nil, false
	}
	return p, &conn, true
}

func (s *Server) handleGetProviderSetup(w http.ResponseWriter, r *http.Request) {
	if s.key == nil {
		s.noEncryptionKey(w)
		return
	}
	p, conn, ok := s.setupConnection(w, r, chiURLParam(r, "id"))
	if !ok {
		return
	}
	out := providerSetupView{ID: p.ID, ExpiresAt: p.ExpiresAt, Ready: conn != nil}
	if conn != nil {
		out.Name, out.Endpoint = conn.Name, conn.Endpoint
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) resolveProviderSetup(w http.ResponseWriter, r *http.Request, in *providerInput) bool {
	if in.SetupID == nil || *in.SetupID == "" {
		return true
	}
	if s.key == nil {
		s.noEncryptionKey(w)
		return false
	}
	_, conn, ok := s.setupConnection(w, r, *in.SetupID)
	if !ok {
		return false
	}
	if conn == nil {
		unprocessable(w, "Run the setup command on the Proxmox host first.", []fieldError{{"setup_id", "Waiting for the Proxmox host."}})
		return false
	}
	// A staged credential is bound to the endpoint and CA it was enrolled with.
	// It must never be reusable with an endpoint supplied by a different draft.
	kind := store.ProviderProxmox
	private := "tailcat"
	verify := false
	in.Kind, in.Name, in.Endpoint = &kind, &conn.Name, &conn.Endpoint
	in.Credential, in.CAPEM = &conn.Credential, &conn.CAPEM
	in.Connection, in.TailcatAddress, in.InsecureSkipVerify = &private, &conn.TailcatAddress, &verify
	return true
}
