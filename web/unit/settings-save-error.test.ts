import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ApiError } from '../src/lib/api/client.ts';
import { settingSaveError } from '../src/lib/errors.ts';

const KEY = 'server.external_url';

function refusal(
  message: string,
  errors: { field: string; message: string }[] = [],
  status = 422,
): ApiError {
  return new ApiError({ status, code: 'unprocessable', message, errors });
}

// The Settings page and the Connect dialog both save a setting, and they must
// describe the same refusal in the same words -- which is why this is one
// function and not a copy in each.
test('a refusal about the setting itself is shown in the API’s own words', () => {
  const why =
    'This belongs to whoever runs this controller rather than to the fleet. Ask them to change it.';
  const cause = refusal('these settings could not be changed', [{ field: KEY, message: why }]);
  assert.equal(settingSaveError(cause, KEY), why);
});

// A request the whole of which would leave a controller that will not start
// names no field of its own. Its reason is the useful half, so it is kept after
// the envelope's summary rather than dropped for it.
test('a refusal of the whole request keeps its reason beside the summary', () => {
  const cause = refusal('that would leave a controller that will not start', [
    { field: '', message: 'oidc.issuer is required when single sign-on is on.' },
  ]);
  assert.equal(
    settingSaveError(cause, KEY),
    'that would leave a controller that will not start: oidc.issuer is required when single sign-on is on.',
  );
});

test('a refusal with no field errors is the API’s summary and nothing else', () => {
  // A 403 names the role required, and paraphrasing it would lose that.
  const cause = refusal('this needs the admin role', [], 403);
  assert.equal(settingSaveError(cause, KEY), 'this needs the admin role');
});

// Not an ApiError at all -- something in the browser threw -- so there is no
// sentence of the API's to pass on, and the operator is told whom to hand the
// request ID to rather than shown a stack.
test('a failure that is not the API’s says so and says who to ask', () => {
  const said = settingSaveError(new TypeError('boom'), KEY);
  assert.match(said, /^That change could not be made\./);
  assert.match(said, /request ID/);
});
