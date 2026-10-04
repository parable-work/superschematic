package rustsdkgen

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// encryptedBodyService is apigen's TypeScript schema with an Encrypted
// operation set: openWallet takes an input type, resetPin a path parameter
// and scalar arguments.
const encryptedBodyService = "encrypted-body-api"

// TestEncryptedBodiesAreTheRequestBody generates the Rust types crate and
// the Rust SDK crate of encrypted-body-api, checks it with cargo clippy
// and runs encryptedBodySDKTest with cargo test: each call sends an
// envelope that the test's private key opens, for both algorithms, to the
// request body itself, as the TypeScript and Python SDKs send it and the Go
// server's payload decryptor expects. The key pair is generated here and
// written into the test.
// CARGO_TARGET_DIR is honored when set.
func TestEncryptedBodiesAreTheRequestBody(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", encryptedBodyService))
	if err != nil {
		t.Fatalf("load %s: %v", encryptedBodyService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: encryptedBodyService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", encryptedBodyService)
	sdkDir := filepath.Join(root, "sdk", "rust", encryptedBodyService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: encryptedBodyService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(encryptedBodyService), naming.Default().RustTypesCrate(encryptedBodyService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, naming.Default()))
	privateKey, publicKey := testKeyPair(t)
	test := strings.NewReplacer(
		"SDK_CRATE", strings.ReplaceAll(sdkOutput.CrateName, "-", "_"),
		"PRIVATE_KEY_PEM", privateKey,
		"PUBLIC_KEY_PEM", publicKey,
	).Replace(encryptedBodySDKTest)
	writeFile(t, filepath.Join(sdkDir, "tests", "encrypted_body.rs"), test)

	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// testKeyPair returns a new RSA key pair as a PKCS #8 private key PEM and a
// SubjectPublicKeyInfo public key PEM.
func testKeyPair(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}))
}

// encryptedBodySDKTest is tests/encrypted_body.rs of the generated SDK
// crate, with SDK_CRATE replaced by the crate's module name and the key
// placeholders by the test's key pair.
const encryptedBodySDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use base64::prelude::BASE64_STANDARD;
use base64::Engine;
use rsa::pkcs8::DecodePrivateKey;
use rsa::{Oaep, RsaPrivateKey};
use serde_json::{json, Value};
use sha2::Sha256;
use SDK_CRATE::namespaces::wallet::ResetPinInput;
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, EncryptedBodyApiSdk, EncryptedRequestOptions, PublicEncryptionKey};

const PRIVATE_KEY: &str = r#"PRIVATE_KEY_PEM"#;
const PUBLIC_KEY: &str = r#"PUBLIC_KEY_PEM"#;

/// Answers one request per canned value with the success envelope around
/// it, and sends each request's method, path and JSON body back.
fn serve(responses: Vec<Value>) -> (String, mpsc::Receiver<(String, String, Value)>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for data in responses {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            let mut parts = request_line.split_whitespace();
            let method = parts.next().unwrap_or_default().to_string();
            let path = parts.next().unwrap_or_default().to_string();
            let mut length = 0usize;
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                let header = header.trim_end();
                if header.is_empty() {
                    break;
                }
                if let Some((name, value)) = header.split_once(':') {
                    if name.eq_ignore_ascii_case("content-length") {
                        length = value.trim().parse().unwrap();
                    }
                }
            }
            let mut body = vec![0u8; length];
            reader.read_exact(&mut body).unwrap();
            let body = if body.is_empty() { Value::Null } else { serde_json::from_slice(&body).unwrap() };
            sender.send((method, path, body)).unwrap();
            let payload = json!({"data": data, "meta": {"requestId": "req-1"}}).to_string();
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                payload.len(),
                payload
            )
            .unwrap();
        }
    });
    (base_url, receiver)
}

/// Opens an envelope as the Go server's payload decryptor does and returns
/// the plaintext as JSON.
fn open(envelope: &Value) -> Value {
    let key = RsaPrivateKey::from_pkcs8_pem(PRIVATE_KEY).unwrap();
    let field = |name: &str| BASE64_STANDARD.decode(envelope[name].as_str().unwrap()).unwrap();
    let plaintext = match envelope["algorithm"].as_str().unwrap() {
        "RSA_OAEP_256" => key.decrypt(Oaep::new::<Sha256>(), &field("payload")).unwrap(),
        "AES_256_GCM_RSA_OAEP_256" => {
            let aes_key = key.decrypt(Oaep::new::<Sha256>(), &field("encryptedKey")).unwrap();
            let cipher = Aes256Gcm::new_from_slice(&aes_key).unwrap();
            cipher.decrypt(Nonce::from_slice(&field("iv")), field("payload").as_ref()).unwrap()
        }
        other => panic!("unexpected algorithm {}", other),
    };
    serde_json::from_slice(&plaintext).unwrap()
}

#[tokio::test]
async fn encrypted_bodies_are_the_request_body() {
    for algorithm in ["RSA_OAEP_256", "AES_256_GCM_RSA_OAEP_256"] {
        let (base_url, requests) = serve(vec![
            json!({"owner": "ada"}),
            json!({"owner": "w-1", "hint": "birthday"}),
        ]);
        let sdk = EncryptedBodyApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
        let options = EncryptedRequestOptions {
            public_encryption_key: Some(PublicEncryptionKey {
                public_key: PUBLIC_KEY.to_string(),
                algorithm: algorithm.to_string(),
                key_id: "key-1".to_string(),
            }),
            request_options: None,
        };

        let opened = sdk
            .wallet
            .open_wallet(types::OpenWalletInput { owner: "ada".to_string(), pin: "1234".to_string() }, Some(&options))
            .await
            .unwrap();
        let (method, path, envelope) = requests.recv().unwrap();
        assert_eq!((method.as_str(), path.as_str()), ("POST", "/api/wallets"));
        assert_eq!(envelope["algorithm"], algorithm);
        assert_eq!(envelope["keyId"], "key-1");
        assert_eq!(open(&envelope), json!({"owner": "ada", "pin": "1234"}), "{}", algorithm);
        assert_eq!(opened.owner, "ada");

        sdk.wallet
            .reset_pin(
                "w-1".to_string(),
                ResetPinInput { pin: "5678".to_string(), hint: Some("birthday".to_string()) },
                Some(&options),
            )
            .await
            .unwrap();
        let (method, path, envelope) = requests.recv().unwrap();
        assert_eq!((method.as_str(), path.as_str()), ("PUT", "/api/wallets/w-1/pin"));
        assert_eq!(open(&envelope), json!({"pin": "5678", "hint": "birthday"}), "{}", algorithm);
    }
}
`
