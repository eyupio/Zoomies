// The updates fixture: the monitoring fixture's demo fleet, as a release build
// that is behind two releases.
//
// The binary under test is a `dev` build, which no release comparison accepts,
// so on the shared fixture the Updates page can only say that the build is left
// alone. ZOOMIES_SEED_UPDATES makes this controller report 1.3.0 and read a
// release list that holds v1.3.1 and v1.3.2, and manual mode keeps the sentence
// it shows from moving with the clock the way an auto soak's would. The
// environment pins the mode, so a spec cannot change it; the soak is free.

import { mkdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { serveController } from './controller.mjs';

const port = process.argv[2] ?? '8094';

// The variable's value is the folder the controller will do its updating
// through, and it is made here, at a path derived from the port, so that a spec
// can work out where it is without being told. Made afresh each time: what a
// last run left in it is not what this one starts from. Only its owner may read
// it, because the name is predictable and the directory is shared.
const updateFolder = join(tmpdir(), `zoomies-e2e-update-folder-${port}`);
rmSync(updateFolder, { recursive: true, force: true });
mkdirSync(updateFolder, { mode: 0o700 });
process.on('exit', () => rmSync(updateFolder, { recursive: true, force: true }));

serveController({
  port,
  prefix: 'zoomies-e2e-updates-',
  env: {
    ZOOMIES_DISABLE_AUTH: 'true',
    ZOOMIES_SEED_DEMO: 'true',
    ZOOMIES_UPDATE_MODE: 'manual',
    ZOOMIES_SEED_UPDATES: updateFolder,
  },
});
