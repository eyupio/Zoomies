import { serveController } from './controller.mjs';

serveController({
  port: process.argv[2] ?? '8093',
  prefix: 'zoomies-transfer-',
  env: { ZOOMIES_DISABLE_AUTH: 'true' },
});
