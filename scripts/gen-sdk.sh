#!/usr/bin/env bash
# Regenerates the TypeScript SDK in sdk/nodejs from the plugin's own schema. CI runs this and fails
# when the committed SDK differs, so run it after any change to the provider's resources.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/provider/bin/pulumi-resource-github-runners"
(cd "$root/provider" && go build -o "$bin" ./cmd/pulumi-resource-github-runners)
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
pulumi package gen-sdk "$bin" --language nodejs --out "$out"
rm -rf "$root/sdk/nodejs"
mkdir -p "$root/sdk"
mv "$out/nodejs" "$root/sdk/nodejs"
# Consumers type-check these sources with their own settings, and the generated `isInstance`
# overrides a base-class member without saying so, which `noImplicitOverride` rejects.
find "$root/sdk/nodejs" -name '*.ts' -exec sed -i.bak 's/public static isInstance(/public static override isInstance(/' {} +
find "$root/sdk/nodejs" -name '*.bak' -delete
# Consumers link this directory and load it as TypeScript, so main points at the source. The
# generator leaves the version as the literal "${VERSION}"; the committed SDK is the development
# build, matched by a plugin built from this checkout and installed as 0.0.0-dev.
node -e '
const f = process.argv[1], p = JSON.parse(require("fs").readFileSync(f, "utf8"));
p.main = "index.ts";
p.version = "0.0.0-dev";
if (p.pulumi) p.pulumi.version = "0.0.0-dev";
require("fs").writeFileSync(f, JSON.stringify(p, null, 4) + "\n");' "$root/sdk/nodejs/package.json"
