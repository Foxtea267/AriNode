#!/usr/bin/env python3
"""Create the small XBoard 4f48e61 companion patch from its unmodified source."""
import argparse
import difflib
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--source", type=Path, required=True, help="Unmodified XBoard 4f48e61 source root")
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
repo = Path(__file__).resolve().parent.parent
changes = {}

def replace(name, old, new):
    original, current = changes.get(name, ((args.source / name).read_text(encoding="utf-8"),) * 2)
    if current.count(old) != 1:
        raise SystemExit(f"Expected exact 4f48e61 source context in {name}")
    changes[name] = original, current.replace(old, new, 1)

replace("app/Protocols/General.php", """        $params = [
            'sni' => data_get($protocol_settings, 'tls.server_name'),
            'insecure' => data_get($protocol_settings, 'tls.allow_insecure')
""", """        $params = [
            'security' => 'tls',
            'sni' => data_get($protocol_settings, 'tls.server_name'),
            'insecure' => data_get($protocol_settings, 'tls.allow_insecure')
""")
replace("app/Services/ServerService.php", """        return $response;
    }

    /**
     * 根据协议类型和标识获取服务器""", """        return \\App\\Support\\NodeTlsConfig::stable($response);
    }

    /**
     * 根据协议类型和标识获取服务器""")
replace("app/Http/Requests/Admin/ServerSave.php", "    public function attributes(): array", """    public function after(): array
    {
        return [function (\\Illuminate\\Validation\\Validator $validator): void {
            if ($validator->errors()->isNotEmpty()) { return; }
            $existing = $this->input('id') ? Server::find($this->input('id')) : null;
            $cert = $this->has('cert_config') ? $this->input('cert_config') : $existing?->cert_config;
            $settings = $this->input('protocol_settings', $existing?->protocol_settings ?? []);
            $name = data_get($settings, 'tls.server_name') ?? data_get($settings, 'tls_settings.server_name') ?? data_get($settings, 'server_name');
            try { \\App\\Support\\NodeTlsConfig::validateContent($cert, $name); }
            catch (\\InvalidArgumentException $e) { $validator->errors()->add('cert_config', $e->getMessage()); }
        }];
    }

    public function attributes(): array""")
helper = "app/Support/NodeTlsConfig.php"
changes[helper] = "", (repo / "integrations/xboard/patches/NodeTlsConfig.php").read_text(encoding="utf-8")
patch = "".join("".join(difflib.unified_diff(old.splitlines(keepends=True), new.splitlines(keepends=True),
              fromfile="a/" + name if old else "/dev/null", tofile="b/" + name))
              for name, (old, new) in changes.items())
out = repo / "integrations/xboard/patches/4f48e61-tls-certificates.patch"
if args.check:
    if out.read_text(encoding="utf-8") != patch:
        raise SystemExit("Stale XBoard TLS patch; rebuild it")
else:
    out.write_text(patch, encoding="utf-8", newline="\n")
print("XBoard 4f48e61 TLS companion patch " + ("verified" if args.check else "built"))
