//! A fake Rust server that honours the contract of a generated entrypoint
//! (docs/stack-model.md, section 8.1) for the local target's integration
//! test (D57): it listens on $PORT, answers /healthz and /readyz, echoes
//! its environment at /env as a JSON object, and on SIGTERM stops and says
//! so. It uses the standard library alone.

use std::env;
use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};

fn main() {
    let port = env::var("PORT").expect("PORT is set");
    let listener = TcpListener::bind(format!("127.0.0.1:{port}")).expect("listen on $PORT");
    on_sigterm();
    println!("fake Rust server listening on 127.0.0.1:{port}");
    for stream in listener.incoming() {
        if let Ok(stream) = stream {
            serve(stream);
        }
    }
}

/// serve answers one request, and closes the connection.
fn serve(mut stream: TcpStream) {
    let mut reader = BufReader::new(&stream);
    let mut request = String::new();
    if reader.read_line(&mut request).is_err() {
        return;
    }
    // The headers, up to the blank line, which no route reads.
    let mut line = String::new();
    while reader.read_line(&mut line).map(|n| n > 2).unwrap_or(false) {
        line.clear();
    }
    let path = request.split_whitespace().nth(1).unwrap_or("/");
    let (status, body) = match path {
        "/healthz" | "/readyz" => ("200 OK", "ok".to_string()),
        "/env" => ("200 OK", environment()),
        _ => ("404 Not Found", "not found".to_string()),
    };
    let _ = write!(
        stream,
        "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
}

/// environment is the process's environment as a JSON object.
fn environment() -> String {
    let members: Vec<String> = env::vars()
        .map(|(name, value)| format!("{}:{}", quote(&name), quote(&value)))
        .collect();
    format!("{{{}}}", members.join(","))
}

/// quote is s as a JSON string.
fn quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// on_sigterm makes SIGTERM print that the server stopped, then exit 0, as
/// a generated entrypoint drains and exits.
#[cfg(unix)]
fn on_sigterm() {
    extern "C" {
        fn signal(signum: i32, handler: extern "C" fn(i32)) -> usize;
        fn write(fd: i32, buf: *const std::ffi::c_void, count: usize) -> isize;
        fn _exit(status: i32) -> !;
    }
    extern "C" fn stop(_: i32) {
        let message = b"fake Rust server stopped\n";
        // write and _exit are safe in a signal handler; println! is not.
        unsafe {
            write(1, message.as_ptr().cast(), message.len());
            _exit(0);
        }
    }
    const SIGTERM: i32 = 15;
    unsafe {
        signal(SIGTERM, stop);
    }
}

#[cfg(not(unix))]
fn on_sigterm() {}
