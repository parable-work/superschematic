//! The TLS a connection opens with, honouring its `sslmode`: rustls on
//! ring, checking as much of the server's certificate as [`Verify`] says,
//! against the system's roots.

use std::fmt;
use std::sync::{Arc, OnceLock};

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::WebPkiServerVerifier;
use rustls::crypto::{verify_tls12_signature, verify_tls13_signature, CryptoProvider};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{
    CertificateError, ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme,
};

use crate::sslmode::{read_ssl_mode, Verify};

/// What a connection's TLS connects with: a `MakeTlsConnect` the
/// synchronous `postgres` crate's `Config::connect` and tokio-postgres's
/// both take.
pub type Tls = tokio_postgres_rustls::MakeRustlsConnect;

/// Why a connection's TLS could not be made: the system has no roots to
/// verify a certificate with, or rustls refused the configuration.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TlsError(String);

impl fmt::Display for TlsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "the database's TLS: {}", self.0)
    }
}

impl std::error::Error for TlsError {}

/// The connection string as tokio-postgres reads it ([`read_ssl_mode`])
/// and the TLS that verifies what its `sslmode` asks. Connect with both:
/// `string.parse::<postgres::Config>()?.connect(tls)`.
pub fn tls_for(url: &str) -> Result<(String, Tls), TlsError> {
    let (url, verify) = read_ssl_mode(url);
    Ok((url, tls(verify)?))
}

/// The TLS that checks as much of the server's certificate as `verify`
/// says. Whether a connection uses it at all is its `sslmode`'s to say:
/// `disable` never does, `prefer` when the server offers TLS.
pub fn tls(verify: Verify) -> Result<Tls, TlsError> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let verifier: Arc<dyn ServerCertVerifier> = match verify {
        Verify::Nothing => Arc::new(Unchecked(Arc::clone(&provider))),
        Verify::Chain => Arc::new(ChainOnly(web_pki(&provider)?)),
        Verify::Host => web_pki(&provider)?,
    };
    let config = ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|error| TlsError(error.to_string()))?
        .dangerous()
        .with_custom_certificate_verifier(verifier)
        .with_no_client_auth();
    Ok(Tls::new(config))
}

/// The verifier of a certificate's chain and host against the system's
/// roots.
fn web_pki(provider: &Arc<CryptoProvider>) -> Result<Arc<WebPkiServerVerifier>, TlsError> {
    WebPkiServerVerifier::builder_with_provider(system_roots()?, Arc::clone(provider))
        .build()
        .map_err(|error| TlsError(error.to_string()))
}

/// The system's roots, read once for the process: the first connection
/// that verifies reads them.
fn system_roots() -> Result<Arc<RootCertStore>, TlsError> {
    static ROOTS: OnceLock<Result<Arc<RootCertStore>, TlsError>> = OnceLock::new();
    ROOTS
        .get_or_init(|| {
            let found = rustls_native_certs::load_native_certs();
            let mut roots = RootCertStore::empty();
            roots.add_parsable_certificates(found.certs);
            if roots.is_empty() {
                let why = found
                    .errors
                    .first()
                    .map_or_else(String::new, |error| format!(" ({error})"));
                return Err(TlsError(format!(
                    "the system holds no root certificate to verify the server's with{why}"
                )));
            }
            Ok(Arc::new(roots))
        })
        .clone()
}

/// A verifier that takes any certificate, as `prefer` and `require` do,
/// while still checking that the server holds its key.
#[derive(Debug)]
struct Unchecked(Arc<CryptoProvider>);

impl ServerCertVerifier for Unchecked {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls12_signature(
            message,
            cert,
            dss,
            &self.0.signature_verification_algorithms,
        )
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls13_signature(
            message,
            cert,
            dss,
            &self.0.signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.0.signature_verification_algorithms.supported_schemes()
    }
}

/// A verifier of the chain alone, as `verify-ca` asks: the web PKI's, a
/// certificate that is not for the host taken. The web PKI checks the
/// chain before the name, so a name it refuses is a chain it took.
#[derive(Debug)]
struct ChainOnly(Arc<WebPkiServerVerifier>);

impl ServerCertVerifier for ChainOnly {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        match self
            .0
            .verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now)
        {
            Err(rustls::Error::InvalidCertificate(
                CertificateError::NotValidForName | CertificateError::NotValidForNameContext { .. },
            )) => Ok(ServerCertVerified::assertion()),
            verified => verified,
        }
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        self.0.verify_tls12_signature(message, cert, dss)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        self.0.verify_tls13_signature(message, cert, dss)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.0.supported_verify_schemes()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_connector_that_verifies_nothing_needs_no_roots() {
        assert!(tls(Verify::Nothing).is_ok());
    }

    #[test]
    fn a_string_keeps_its_text_and_takes_the_tls_its_sslmode_asks() {
        let (url, _) = tls_for("postgres://u@h/d?sslmode=disable").expect("the TLS");
        assert_eq!(url, "postgres://u@h/d?sslmode=disable");
        let (url, _) = tls_for("host=h sslmode=prefer").expect("the TLS");
        assert_eq!(url, "host=h sslmode=prefer");
    }
}
