// Verification helper: substitutes the module placeholders the plugin injects at
// runtime, then runs `node --check` on the console's inline script so a syntax
// error cannot reach a release. Run with `node tests/inline-script-syntax.cjs`;
// it is not a test file and `node --test` does not collect it.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const root = path.join(__dirname, '..', 'internal/bridge/ui');
let html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const modules = {
  '/*__CPA_AUTH_COMPAT__*/': 'cpa-auth.js',
  '/*__SCHEDULING_MODULE__*/': 'scheduling.js',
  '/*__ERROR_RULES_MODULE__*/': 'error-rules.js',
  '/*__RELEASE_NOTICE_MODULE__*/': 'release-notice.js',
};
for (const [marker, file] of Object.entries(modules)) {
  html = html.replace(marker, fs.readFileSync(path.join(root, file), 'utf8'));
}
html = html.replace('__PLANBRIDGE_API_BASE__', '/v0/management/planbridge');
html = html.replace('__PLANBRIDGE_LOGO_DATA__', 'data:image/png;base64,');

const scripts = [...html.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi)].map(match => match[1]);
if (!scripts.length) {
  console.error('no inline script found');
  process.exit(1);
}
const file = path.join(os.tmpdir(), `planbridge-inline-${process.pid}.js`);
fs.writeFileSync(file, scripts.join('\n;\n'));
try {
  execFileSync(process.execPath, ['--check', file], { stdio: 'inherit' });
  console.log(`inline script syntax ok (${scripts.length} block(s))`);
} finally {
  fs.rmSync(file, { force: true });
}
