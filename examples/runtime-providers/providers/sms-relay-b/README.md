# sms-relay-b

A second SMS channel adapter (D159), for a relay of another shape. It
sends with a form post to `<URL>/accounts/<ACCOUNT>/send`, with basic
authentication of the account and its token (`TOKEN`, a secret), and
takes no callbacks. An environment swaps sms-relay-a for it by its config
alone: the slot's value, and `ACCOUNT`, which sms-relay-a does not read.

Twilio's Messages API, form posts under an account SID with basic
authentication, is the shape this adapter could front. The example's test
talks to a stub.
