# Security policy

## Reporting a vulnerability

Do not open a public issue for a security problem. Report it privately by
email to TODO-CONTACT (replace with a monitored security address before the
repository goes public). If GitHub private vulnerability reporting is enabled
on this repository, the "Report a vulnerability" button on the Security tab
reaches the same people.

Include what you can: affected package and version, a minimal reproduction,
and the impact you believe it has. You will get an acknowledgement within
three business days and a status update at least every two weeks until the
report is closed.

## Disclosure window

We aim to publish a fix and an advisory within 90 days of the report. If a fix
needs longer, we will tell you why and agree a new date with you. Once the fix
is released, we credit the reporter in the advisory unless you ask us not to.

## Supported versions

Only the latest minor release line receives security fixes. Older lines are
not patched; upgrade to the current minor to receive fixes.

| Version                       | Supported |
| ----------------------------- | --------- |
| Latest minor (`vX.Y.*`)       | Yes       |
| Earlier minors and majors     | No        |

## Scope

In scope: the compiler and CLI, the Go, TypeScript and Python schema
runtimes, the Go and Rust HTTP runtimes, the TypeScript authoring packages,
and the code they generate. Out of scope: vulnerabilities in third-party
dependencies that do not affect this project's behaviour (report those
upstream, superscalar included), and issues that require a compromised build
machine or registry account.
