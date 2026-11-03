//! Comments and a principal acting for a subject (D77 step 4,
//! `comments-contract.md`, "Batch 4"): a post and a begin record the
//! subject as the message's `for`, which a finish, an edit and the
//! schedule's interrupt keep; each edit, reaction and resolve records its
//! caller's. Acting for someone grants nothing: the agent is the author,
//! the reactor and the reader, and neither it nor the subject passes the
//! other's author checks. The subject follows the thread of a message
//! written for it, hears nothing of that message, and finds it unread.
//! On SQLite and, with the variable set, on Postgres.

mod comments_support;
mod support;

use std::time::{Duration, Instant};

use serde_json::{json, Value};

use comments_support::{
    agent, agent_alone, agent_for_bob, bob, carol, notifier, review_config, sorted, vetoed, world,
};
use support::{alice, targets};

const PATIENCE: Duration = Duration::from_secs(20);

#[test]
fn a_message_records_whom_it_was_written_for_and_keeps_it() {
    for target in targets("comments_for_records") {
        let world = world(&target, json!({"composeTimeoutMs": 8000}));
        let posted = world.ok(&agent(), "post", json!({"body": "Looks good to me."}));
        assert_eq!(
            (&posted["author"], &posted["for"]),
            (&json!("agent:reviewer"), &json!("alice"))
        );
        // The record's members: for after author.
        let names: Vec<&String> = posted.as_object().unwrap().keys().collect();
        assert_eq!(names[..4], ["id", "author", "for", "state"]);

        // A begin records it; a finish acting for another keeps it.
        let begun = world.ok(&agent(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        assert_eq!(world.message(begun)["for"], json!("alice"));
        let finished = world.ok(
            &agent_for_bob(),
            "finish",
            json!({"message": begun, "body": "Done."}),
        );
        assert_eq!(finished["for"], json!("alice"));

        // The schedule's interrupt, as the runner, keeps it.
        let idle = world.ok(&agent(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        world.ok(
            &agent(),
            "appendDraft",
            json!({"message": idle, "text": "Half"}),
        );
        world.set_clock(20_000);
        let deadline = Instant::now() + PATIENCE;
        while world.message(idle)["state"] == "composing" {
            assert!(Instant::now() < deadline, "the schedule never interrupted");
            world.engine.runner().run_due().unwrap();
            world.engine.clock().advance(1000).unwrap();
        }
        let interrupted = world.message(idle);
        assert_eq!(
            (&interrupted["state"], &interrupted["for"]),
            (&json!("interrupted"), &json!("alice"))
        );

        // A principal acting for nobody records none.
        let plain = world.ok(&agent_alone(), "post", json!({"body": "Alone."}));
        assert!(plain.get("for").is_none(), "{plain}");

        // Each event's for is its call's: the finish's bob, the runner's
        // none.
        let wrote: Vec<Value> = world
            .events()
            .iter()
            .map(|event| json!([event.change["operation"], event.actor, event.for_subject]))
            .collect();
        assert_eq!(
            wrote,
            [
                json!(["post", "agent:reviewer", "alice"]),
                json!(["begin", "agent:reviewer", "alice"]),
                json!(["finish", "agent:reviewer", "bob"]),
                json!(["begin", "agent:reviewer", "alice"]),
                json!(["interrupt", "runner", null]),
                json!(["post", "agent:reviewer", null]),
            ]
        );
        world.engine.shutdown();
    }
}

#[test]
fn acting_for_someone_passes_no_author_check() {
    for target in targets("comments_for_authors") {
        let world = world(&target, json!({}));
        let hers = world.say(&alice(), "Mine.");
        for (operation, params) in [
            ("edit", json!({"message": hers, "body": "Changed."})),
            ("deleteMessage", json!({"message": hers})),
            ("resolve", json!({"thread": hers})),
        ] {
            let (code, details) = vetoed(world.call(&agent(), operation, params));
            assert_eq!(
                (code.as_str(), details),
                ("not_author", json!({"message": hers, "author": "alice"})),
                "{operation}"
            );
        }
        let draft = world.ok(&alice(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        for operation in ["finish", "appendDraft"] {
            let params = if operation == "finish" {
                json!({"message": draft, "body": "Not mine to finish."})
            } else {
                json!({"message": draft, "text": "x"})
            };
            assert_eq!(
                vetoed(world.call(&agent(), operation, params)).0,
                "not_author"
            );
        }
        assert_eq!(
            vetoed(world.call(&agent(), "interrupt", json!({"message": draft}))).0,
            "not_idle"
        );

        // Alice may not edit what the agent wrote for her.
        let written = world.say(&agent(), "For Alice.");
        let (code, details) = vetoed(world.call(
            &alice(),
            "edit",
            json!({"message": written, "body": "Mine now."}),
        ));
        assert_eq!(
            (code.as_str(), details),
            (
                "not_author",
                json!({"message": written, "author": "agent:reviewer"})
            )
        );
        // The agent may, acting for Bob: the edit is Bob's, the message
        // stays Alice's.
        let edited = world.ok(
            &agent_for_bob(),
            "edit",
            json!({"message": written, "body": "For Alice, again."}),
        );
        assert_eq!(edited["for"], json!("alice"));
        let edits = world.ok(&alice(), "listEdits", json!({"message": written}));
        assert_eq!(
            edits["items"][0],
            json!({"edit": 1, "body": "For Alice.", "blocks": [], "mentions": [], "by": "agent:reviewer", "for": "bob", "at": 1000})
        );
        let event = world.events().pop().unwrap();
        assert_eq!(
            (event.change["operation"].clone(), event.for_subject.clone()),
            (json!("edit"), Some("bob".to_owned()))
        );
        assert_eq!(event.change["result"]["for"], json!("alice"));
        world.engine.shutdown();
    }
}

#[test]
fn the_subject_follows_hears_nothing_of_the_message_and_finds_it_unread() {
    for target in targets("comments_for_follows") {
        let world = world(&target, review_config());
        let thread = world.say(&bob(), "Thread.");
        world.ok(&alice(), "markRead", json!({"upTo": thread}));
        world.ok(&alice(), "follow", json!({"thread": thread, "on": true}));
        // Written for Alice, mentioning her and Carol, in a thread she
        // follows: Bob hears of it as a follower, Carol as mentioned,
        // Alice not at all.
        let reply = world.post(
            &agent(),
            json!({"body": "Found two issues.", "thread": thread, "mentions": ["alice", "carol"]}),
        );
        // The next reply reaches her.
        let next = world.reply(&carol(), thread, "Which ones?");
        // A top-level message written for her: her conversation marker and
        // thread marker stay; the agent's move.
        let top = world.say(&agent(), "Summary for Alice.");
        assert_eq!(
            world.deliveries(),
            sorted(vec![
                json!(["Review", "r1", reply, "bob", 0, 1, "pending"]),
                json!(["Review", "r1", reply, "carol", 1, 0, "pending"]),
                json!(["Review", "r1", next, "agent:reviewer", 0, 1, "pending"]),
                json!(["Review", "r1", next, "alice", 0, 1, "pending"]),
                json!(["Review", "r1", next, "bob", 0, 1, "pending"]),
            ])
        );
        assert_eq!(
            world.follows(),
            sorted(vec![
                json!(["Review", "r1", thread, "agent:reviewer", 1, 2]),
                json!(["Review", "r1", thread, "alice", 1, 1]),
                json!(["Review", "r1", thread, "bob", 1, 1]),
                json!(["Review", "r1", thread, "carol", 1, 3]),
                json!(["Review", "r1", top, "agent:reviewer", 1, 4]),
                json!(["Review", "r1", top, "alice", 1, 4]),
            ])
        );
        assert_eq!(
            world.reads(),
            sorted(vec![
                json!(["Review", "r1", "agent:reviewer", 4]),
                json!(["Review", "r1", "alice", 1]),
                json!(["Review", "r1", "bob", 1]),
            ])
        );
        assert_eq!(world.fields(&alice())["unread"], json!(1));
        assert_eq!(world.fields(&agent())["unread"], json!(0));

        // Her inbox: the thread counts the reply written for her, the
        // conversation the top-level one, with its for.
        let inbox = world.ns_ok(&alice(), "inbox", json!({}));
        assert_eq!(
            inbox["items"],
            json!([
                {"kind": "thread", "schema": "Review", "instanceId": "r1", "message": next, "thread": thread,
                 "author": "carol", "postedAt": 1000, "excerpt": "Which ones?", "unread": 2},
                {"kind": "conversation", "schema": "Review", "instanceId": "r1", "message": top,
                 "author": "agent:reviewer", "for": "alice", "postedAt": 1000, "excerpt": "Summary for Alice.", "unread": 1},
            ])
        );
        assert_eq!(
            world.ok(
                &alice(),
                "markRead",
                json!({"thread": thread, "upTo": reply})
            ),
            json!({"readUpTo": 2, "unread": 1})
        );

        // Carol's mention item and the claim name whom it was for.
        let carols = world.ns_ok(&carol(), "inbox", json!({"kinds": ["mention"]}));
        assert_eq!(carols["items"][0]["for"], json!("alice"));
        // Carol's is dropped as read: she replied after it.
        let claimed = world.ns_ok(&notifier(), "claimDeliveries", json!({"limit": 10}));
        let fors: Vec<Value> = claimed["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| json!([item["message"], item["subject"], item.get("for")]))
            .collect();
        assert_eq!(
            sorted(fors),
            sorted(vec![
                json!([reply, "bob", "alice"]),
                json!([next, "agent:reviewer", null]),
                json!([next, "alice", null]),
                json!([next, "bob", null]),
            ])
        );
        let item = claimed["items"]
            .as_array()
            .unwrap()
            .iter()
            .find(|item| item["message"] == json!(reply))
            .unwrap();
        let names: Vec<&String> = item.as_object().unwrap().keys().collect();
        let at = names.iter().position(|name| *name == "author").unwrap();
        assert_eq!(names[at + 1], "for");

        // An edit that adds Alice's mention gives her no delivery either.
        world.ok(
            &agent(),
            "edit",
            json!({"message": top, "mentions": ["alice", "bob"]}),
        );
        let on_top: Vec<Value> = world
            .deliveries()
            .into_iter()
            .filter(|row| row[2] == json!(top))
            .collect();
        assert_eq!(
            on_top,
            [json!(["Review", "r1", top, "bob", 1, 0, "pending"])]
        );
        world.engine.shutdown();
    }
}

#[test]
fn a_reaction_a_marker_and_a_resolve_are_the_callers_own() {
    for target in targets("comments_for_state") {
        let world = world(&target, json!({}));
        let message = world.say(&bob(), "React to me.");
        let reacted = world.ok(
            &agent(),
            "react",
            json!({"message": message, "emoji": "+1"}),
        );
        assert_eq!(
            reacted["reactions"],
            json!({"+1": {"count": 1, "mine": true}})
        );
        assert_eq!(
            world.message(message)["reactions"],
            json!({"+1": {"count": 1, "mine": false}})
        );
        // The reaction is the agent's, whomever it acts for.
        let (code, _) = vetoed(world.call(
            &agent_for_bob(),
            "react",
            json!({"message": message, "emoji": "+1"}),
        ));
        assert_eq!(code, "already_reacted");
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": message, "emoji": "+1"})
            )["items"],
            json!([{"subject": "agent:reviewer", "for": "alice", "at": 1000}])
        );
        world.ok(
            &agent_for_bob(),
            "unreact",
            json!({"message": message, "emoji": "+1"}),
        );
        world.ok(
            &agent_alone(),
            "react",
            json!({"message": message, "emoji": "+1"}),
        );
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": message, "emoji": "+1"})
            )["items"],
            json!([{"subject": "agent:reviewer", "at": 1000}])
        );

        // markRead acting for Alice moves the agent's marker.
        world.ok(&agent(), "markRead", json!({"upTo": message}));
        assert_eq!(
            world.reads(),
            sorted(vec![
                json!(["Review", "r1", "agent:reviewer", 1]),
                json!(["Review", "r1", "bob", 1]),
            ])
        );

        // A resolve records its caller's for; a reopen clears it.
        let thread = world.say(&agent(), "Resolve me.");
        let resolved = world.ok(&agent_for_bob(), "resolve", json!({"thread": thread}));
        assert_eq!(
            resolved["resolved"],
            json!({"by": "agent:reviewer", "for": "bob", "at": 1000})
        );
        world.ok(&agent_alone(), "reopen", json!({"thread": thread}));
        let again = world.ok(&agent_alone(), "resolve", json!({"thread": thread}));
        assert_eq!(
            again["resolved"],
            json!({"by": "agent:reviewer", "at": 1000})
        );

        // Acting for oneself is no principal at all.
        assert!(world
            .call(
                &alice().acting_for("alice"),
                "post",
                json!({"body": "Me, for me."})
            )
            .is_err());
        world.engine.shutdown();
    }
}
