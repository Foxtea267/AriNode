<?php

// Exercises patched upstream builders and the save callback with real OpenSSL
// and Illuminate validation. The DB lookup and base FormRequest are boundaries.
namespace { require __DIR__ . '/vendor/autoload.php'; }
namespace App\Models {
    class Server extends \ArrayObject {
        public const TYPE_VMESS='vmess', TYPE_VLESS='vless', TYPE_SHADOWSOCKS='shadowsocks', TYPE_TROJAN='trojan',
            TYPE_HYSTERIA='hysteria', TYPE_ANYTLS='anytls', TYPE_SOCKS='socks', TYPE_TUIC='tuic', TYPE_HTTP='http';
        public static ?self $existing = null;
        public static function find($id) { return self::$existing; }
        public function __construct(array $values) { parent::__construct($values, \ArrayObject::ARRAY_AS_PROPS); }
    }
}
namespace App\Support { class AbstractProtocol {} }
namespace App\Utils {
    class Helper { public static function wrapIPv6($host) { return str_contains($host, ':') ? '[' . $host . ']' : $host; } }
}
namespace Illuminate\Foundation\Http { class FormRequest extends \Illuminate\Http\Request {} }
namespace {
    use App\Models\Server;
    use App\Protocols\General;
    use App\Services\ServerService;
    use App\Support\NodeTlsConfig;
    use App\Http\Requests\Admin\ServerSave;
    use Illuminate\Translation\ArrayLoader;
    use Illuminate\Translation\Translator;
    use Illuminate\Validation\Factory;

    $source = $argv[1] ?? throw new RuntimeException('Pass the patched XBoard source directory');
    require $source . '/app/Support/NodeTlsConfig.php';
    require $source . '/app/Protocols/General.php';
    require $source . '/app/Services/ServerService.php';
    require $source . '/app/Http/Requests/Admin/ServerSave.php';
    function check($ok, $message): void { if (!$ok) { throw new RuntimeException($message); } }
    function reject(callable $action, $message): void {
        try { $action(); } catch (InvalidArgumentException $e) { check(str_contains($e->getMessage(), $message), 'Unexpected validation message'); return; }
        throw new RuntimeException('Expected rejection: ' . $message);
    }
    $temp = tempnam(sys_get_temp_dir(), 'arinode-openssl-');
    file_put_contents($temp, "[req]\ndistinguished_name=dn\nreq_extensions=ext\n[dn]\n[ext]\nsubjectAltName=DNS:*.554733.xyz,IP:127.0.0.1\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth\n");
    try {
        $options = ['config'=>$temp, 'digest_alg'=>'sha256', 'private_key_type'=>OPENSSL_KEYTYPE_EC, 'curve_name'=>'prime256v1', 'x509_extensions'=>'ext'];
        $private = openssl_pkey_new($options);
        $csr = openssl_csr_new(['commonName'=>'*.554733.xyz'], $private, $options);
        $certificate = openssl_csr_sign($csr, null, $private, 1, $options);
        check($certificate && openssl_x509_export($certificate, $pem) && openssl_pkey_export($private, $key, null, $options), 'Cannot create OpenSSL fixture');
        $config = ['cert_mode'=>'content','cert_content'=>$pem,'key_content'=>$key];
        foreach (['jp1.554733.xyz', 'JP1.554733.XYZ.', '127.0.0.1', ''] as $name) { NodeTlsConfig::validateContent($config, $name); }
        foreach (['jp1.standard.novonet.node.554733.xyz','554733.xyz','fake554733.xyz','127.0.0.2','*.554733.xyz'] as $name) {
            reject(fn() => NodeTlsConfig::validateContent($config, $name), 'SAN');
        }
        reject(fn() => NodeTlsConfig::validateContent([...$config, 'key_content'=>'bad key'], 'jp1.554733.xyz'), '私钥不匹配');
        reject(fn() => NodeTlsConfig::validateContent([...$config, 'cert_content'=>''], null), 'content 模式');
        NodeTlsConfig::validateContent(['cert_mode'=>'self'], 'existing.self.example');
        NodeTlsConfig::validateContent(['cert_mode'=>'file'], 'existing.file.example');
        $factory = new Factory(new Translator(new ArrayLoader(), 'en'));
        $validateSave = function (array $input) use ($factory) {
            $request = ServerSave::create('/', 'POST', $input);
            $validator = $factory->make([], []);
            foreach ($request->after() as $after) { $after($validator); }
            return $validator->errors();
        };
        $bad = ['cert_config'=>$config, 'protocol_settings'=>['tls'=>['server_name'=>'jp1.standard.novonet.node.554733.xyz']]];
        check($validateSave($bad)->has('cert_config'), 'Saving mismatched SNI did not warn the administrator');
        $good = ['cert_config'=>$config, 'protocol_settings'=>['tls'=>['server_name'=>'jp1.554733.xyz','allow_insecure'=>false]]];
        check($validateSave($good)->isEmpty(), 'Valid certificate was rejected on save');
        Server::$existing = new Server(['cert_config'=>$config,'protocol_settings'=>[]]);
        check($validateSave(['id'=>244, 'protocol_settings'=>$bad['protocol_settings']])->has('cert_config'), 'SNI edit bypassed validation by omitting cert_config');
        foreach (['anytls', 'tuic', 'hysteria'] as $type) {
            $node = new Server(['type'=>$type, 'server_port'=>25331, 'host'=>'2021.luneza.cc', 'protocol_settings'=>[
                'tls'=>['server_name'=>'jp1.554733.xyz','allow_insecure'=>false], 'padding_scheme'=>['stop=8','0=30-30'],
                'version'=>$type==='hysteria'?2:5,'bandwidth'=>['up'=>100,'down'=>100],'obfs'=>['open'=>false], 'congestion_control'=>'cubic'],
                'route_ids'=>[], 'custom_outbounds'=>[], 'custom_routes'=>[], 'cert_config'=>$config]);
            $first = ServerService::buildNodeConfig($node);
            check($first['cert_config']['cert_content']===$pem && $first['cert_config']['key_content']===$key, 'Certificate lost in upstream node response');
            $settings = $node->protocol_settings; $settings['tls'] = array_reverse($settings['tls'], true); $node->protocol_settings = $settings;
            $node->cert_config = array_reverse($config, true);
            check(json_encode($first) === json_encode(ServerService::buildNodeConfig($node)), 'Node response not byte-stable for equivalent maps');
        }
        $ordered = ['routes'=>[['z'=>2,'a'=>1],['id'=>1]],'padding_scheme'=>['stop=8','0=30-30']];
        $stable = NodeTlsConfig::stable($ordered);
        check($stable['routes'][0]['z']===2 && $stable['padding_scheme']===$ordered['padding_scheme'], 'Sorting reordered routing/padding lists');
        $uri = General::buildAnyTLS('test-password', ['name'=>'test', 'host'=>'2001:db8::1', 'port'=>25331, 'protocol_settings'=>$good['protocol_settings']]);
        parse_str(parse_url(trim($uri), PHP_URL_QUERY), $query);
        check(($query['security']??'')==='tls' && ($query['sni']??'')==='jp1.554733.xyz' && ($query['insecure']??'')==='0', 'AnyTLS share link TLS fields missing');
        echo "XBoard TLS patch tests passed (upstream builders, stable maps, SAN save validation, AnyTLS security=tls)\n";
    } finally { unlink($temp); }
}
