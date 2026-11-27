//! A sandbox provider that runs each run's instance as a local process
//! (D159, D185): the Rust port of `providers/local-sandbox`. The spec's
//! command runs in `WORKDIR` with its env and its secrets as environment
//! variables, which it never logs; `cancel` kills it, `status` reads
//! whether it exited, and `cost` counts its wall time. A stub for tests,
//! not an isolation boundary.

use std::collections::BTreeMap;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::time::{Instant, SystemTime};

use async_trait::async_trait;
use superschematic_http_runtime::providers::{
    ProviderDefinition, ProviderInstance, ProviderSettingSpec, RuntimeInterface, SandboxCost,
    SandboxInstance, SandboxProvider, SandboxSpec, SandboxState, SandboxStatus,
};
use superschematic_http_runtime::BoxError;

struct Run {
    child: Child,
    started: Instant,
    ended: Option<Instant>,
    cancelled: bool,
    exit: Option<i32>,
}

struct Sandbox {
    workdir: Option<String>,
    runs: Mutex<BTreeMap<String, Run>>,
}

impl Sandbox {
    fn with<T>(&self, id: &str, f: impl FnOnce(&mut Run) -> T) -> Result<T, BoxError> {
        let mut runs = self.runs.lock().unwrap();
        let run = runs
            .get_mut(id)
            .ok_or_else(|| format!("local-sandbox: no instance {id}"))?;
        if run.exit.is_none() {
            if let Some(status) = run.child.try_wait()? {
                run.exit = Some(status.code().unwrap_or(-1));
                run.ended = Some(Instant::now());
            }
        }
        Ok(f(run))
    }
}

#[async_trait]
impl SandboxProvider for Sandbox {
    async fn start(&self, spec: &SandboxSpec) -> Result<SandboxInstance, BoxError> {
        let (program, args) = spec
            .command
            .split_first()
            .ok_or("local-sandbox: a spec names the command its instance runs")?;
        let mut command = Command::new(program);
        command
            .args(args)
            .env_clear()
            .envs(&spec.env)
            .envs(spec.secrets.iter().map(|(name, value)| (name, value.reveal())))
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        if let Some(dir) = &self.workdir {
            command.current_dir(dir);
        }
        let child = command.spawn()?;
        let id = format!("{}-{}", spec.run_id, child.id());
        self.runs.lock().unwrap().insert(
            id.clone(),
            Run {
                child,
                started: Instant::now(),
                ended: None,
                cancelled: false,
                exit: None,
            },
        );
        Ok(SandboxInstance {
            id,
            started_at: SystemTime::now(),
        })
    }

    async fn cancel(&self, id: &str) -> Result<(), BoxError> {
        self.with(id, |run| {
            if run.exit.is_none() {
                let _ = run.child.kill();
                let _ = run.child.wait();
                run.cancelled = true;
                run.exit = Some(-1);
                run.ended = Some(Instant::now());
            }
        })
    }

    async fn status(&self, id: &str) -> Result<SandboxStatus, BoxError> {
        self.with(id, |run| SandboxStatus {
            id: id.to_owned(),
            state: match (run.cancelled, run.exit) {
                (true, _) => SandboxState::Cancelled,
                (false, None) => SandboxState::Running,
                (false, Some(0)) => SandboxState::Succeeded,
                (false, Some(_)) => SandboxState::Failed,
            },
            exit_code: if run.cancelled { None } else { run.exit },
            detail: None,
        })
    }

    async fn cost(&self, id: &str) -> Result<SandboxCost, BoxError> {
        self.with(id, |run| {
            let wall = run
                .ended
                .unwrap_or_else(Instant::now)
                .duration_since(run.started)
                .as_secs_f64();
            SandboxCost {
                id: id.to_owned(),
                wall_seconds: wall,
                cpu_seconds: 0.0,
                amount: None,
            }
        })
    }
}

/// The definition a server links.
pub fn definition() -> ProviderDefinition {
    ProviderDefinition::new("local-sandbox", RuntimeInterface::Sandbox, |settings, _| {
        Ok(ProviderInstance::Sandbox(Arc::new(Sandbox {
            workdir: settings.optional_text("WORKDIR")?.map(str::to_owned),
            runs: Mutex::new(BTreeMap::new()),
        })))
    })
    .with_setting("WORKDIR", ProviderSettingSpec::plain().optional())
}
