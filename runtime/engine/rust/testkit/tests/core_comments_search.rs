//! Comments' batch 5 (D77 step 5, `comments-contract.md`, "Batch 5"):
//! code blocks with their own checks and `moveAnchor`, which moves an
//! anchor without an edit and keeps its history; attachment blocks held to
//! the config's `attachments`; `listPinned`; and `searchMessages`, a
//! namespace operation over a full-text index of the standing messages'
//! bodies that every write keeps, kept to the schemas the caller may read.
//! Migrations 5 and 6 on a conversation migration 4 holds, and an engine
//! that composes Comments on a Postgres with no `unaccent` to have. On
//! SQLite and, with the variable set, on Postgres.

mod comments_support;
mod support;

use serde_json::{json, Value};

use comments_support::{
    agent, bob, carol, create, invalid, moderator, publish, vetoed, world, world_with, World,
};
use superschematic_engine::storage::{Dialect, SqlValue};
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, Engine, Hooks, Policy, Principal, Result,
    WriteOptions,
};
use support::{alice, issue, named, targets};

/// Holds Review's anchorPermission.
fn anchor_bot() -> Principal {
    Principal::new("bot", ["comments.anchor"])
}

fn code_block(revision: &str, lines: [i64; 2]) -> Value {
    json!({"kind": "code", "repo": "github.com/acme/shop", "path": "src/orders.rs", "revision": revision, "lines": lines, "snippet": "fn place() {"})
}

fn attachment(uri: &str, media_type: &str, bytes: i64) -> Value {
    json!({"kind": "attachment", "uri": uri, "name": "shot.png", "mediaType": media_type, "bytes": bytes,
           "sha256": "0000000000000000000000000000000000000000000000000000000000000000"})
}

#[test]
fn a_code_block_is_held_to_its_lines_and_a_relative_path() {
    for target in targets("comments_code_blocks") {
        let world = world_with(&target, json!({}), json!({"blocks": ["ref"]}));
        let posted = world.ok(
            &alice(),
            "post",
            json!({"body": "Here.", "blocks": [code_block("9c1f2e7", [40, 48])]}),
        );
        assert_eq!(posted["blocks"], json!([code_block("9c1f2e7", [40, 48])]));
        let names: Vec<&String> = posted["blocks"][0].as_object().unwrap().keys().collect();
        assert_eq!(
            names,
            ["kind", "repo", "path", "revision", "lines", "snippet"]
        );

        let ok = json!({"kind": "action", "title": "Ran the tests", "status": "succeeded"});
        let mut backwards = code_block("9c1f2e7", [48, 40]);
        backwards["path"] = json!("/a");
        assert_eq!(
            invalid(world.call(
                &alice(),
                "post",
                json!({"body": "x", "blocks": [ok.clone(), backwards]})
            )),
            [issue(
                "/blocks/1/lines",
                "must have its first line at most its last"
            )]
        );
        for path in ["/a", "a/", "a//b", "./a", "a/../b", "a\\b"] {
            let mut block = code_block("9c1f2e7", [1, 2]);
            block["path"] = json!(path);
            assert_eq!(
                invalid(world.call(&alice(), "post", json!({"body": "x", "blocks": [block]}))),
                [issue(
                    "/blocks/0/path",
                    "must be a relative path: no leading or trailing /, no \\, and no empty, . or .. part"
                )],
                "{path}"
            );
        }
        // A config that lists only ref refuses a code block.
        let (code, details) = vetoed(world.on(
            &alice(),
            "Memo",
            "m1",
            "post",
            json!({"body": "x", "blocks": [code_block("9c1f2e7", [1, 2])]}),
        ));
        assert_eq!(
            (code.as_str(), details),
            (
                "block_not_allowed",
                json!({"block": 0, "kind": "code", "allowed": ["ref"]})
            )
        );
        world.engine.shutdown();
    }
}

#[test]
fn an_attachment_is_held_to_the_configs_schemes_media_types_and_size() {
    for target in targets("comments_attachments") {
        let review = json!({"attachments": {"schemes": ["https", "bucket"], "mediaTypes": ["image/*", "application/pdf"], "maxBytes": 1000}});
        let world = world_with(
            &target,
            review,
            json!({"attachments": {"schemes": ["bucket"]}}),
        );
        assert_eq!(
            invalid(world.call(
                &alice(),
                "post",
                json!({"body": "x", "blocks": [attachment("https:///a.png", "image/png", 10)]})
            )),
            [issue("/blocks/0/uri", "must be an https URL with a host")]
        );
        let refused = |blocks: Value| {
            vetoed(world.call(&alice(), "post", json!({"body": "x", "blocks": blocks})))
        };
        // The scheme first, though the size fails too.
        assert_eq!(
            refused(json!([attachment("ftp://x/a.png", "image/png", 5000)])),
            (
                "attachment_not_allowed".to_owned(),
                json!({"block": 0, "member": "uri", "value": "ftp", "allowed": ["https", "bucket"]})
            )
        );
        assert_eq!(
            refused(json!([attachment("https://x/a.txt", "text/plain", 5000)])),
            (
                "attachment_not_allowed".to_owned(),
                json!({"block": 0, "member": "mediaType", "value": "text/plain", "allowed": ["image/*", "application/pdf"]})
            )
        );
        // The first attachment by index the config refuses.
        assert_eq!(
            refused(json!([
                attachment("bucket:uploads/a.png", "image/png", 10),
                attachment("https://x/a.pdf", "application/pdf", 5000)
            ])),
            (
                "attachment_not_allowed".to_owned(),
                json!({"block": 1, "member": "bytes", "value": 5000, "allowed": 1000})
            )
        );
        // image/* takes image/png; bucket is a scheme the config lists.
        let posted = world.ok(
            &alice(),
            "post",
            json!({"body": "Shot.", "blocks": [
                attachment("bucket:uploads/a.png", "image/png", 1000),
                {"kind": "action", "title": "Uploaded", "status": "running"}
            ]}),
        );
        let id = posted["id"].as_i64().unwrap();
        // schemes: ["bucket"] is the whole list: https is refused.
        assert_eq!(
            vetoed(world.on(
                &alice(),
                "Memo",
                "m1",
                "post",
                json!({"body": "x", "blocks": [attachment("https://x/a.png", "image/png", 1)]})
            )),
            (
                "attachment_not_allowed".to_owned(),
                json!({"block": 0, "member": "uri", "value": "https", "allowed": ["bucket"]})
            )
        );

        // The config narrows: an edit that keeps the attachment passes,
        // one that adds another the config now refuses does not.
        publish(
            &world.engine,
            "Review",
            &json!({"attachments": {"mediaTypes": ["application/pdf"]}}),
        );
        let edited = world.ok(
            &alice(),
            "edit",
            json!({"message": id, "blocks": [
                attachment("bucket:uploads/a.png", "image/png", 1000),
                {"kind": "action", "title": "Uploaded", "status": "succeeded"}
            ]}),
        );
        assert_eq!(edited["blocks"][1]["status"], json!("succeeded"));
        assert_eq!(
            vetoed(world.call(
                &alice(),
                "edit",
                json!({"message": id, "blocks": [
                    attachment("bucket:uploads/a.png", "image/png", 1000),
                    attachment("https://x/b.png", "image/png", 10)
                ]})
            )),
            (
                "attachment_not_allowed".to_owned(),
                json!({"block": 1, "member": "mediaType", "value": "image/png", "allowed": ["application/pdf"]})
            )
        );
        world.engine.shutdown();
    }
}

#[test]
fn move_anchor_refuses_in_order_and_moves_without_an_edit() {
    for target in targets("comments_move_anchor") {
        let config = json!({"anchorPermission": "comments.anchor", "moderatePermission": "comments.moderate", "editWindowMs": 60000});
        let world = world(&target, config);
        let action = json!({"kind": "action", "title": "Reviewed", "status": "succeeded"});
        let id = world.post(
            &alice(),
            json!({"body": "This loop is off by one.", "blocks": [code_block("9c1f2e7", [40, 48]), action]}),
        );
        let move_ = |who: &Principal, params: Value| world.call(who, "moveAnchor", params);
        // 1: the lines, before anything is read.
        assert_eq!(
            invalid(move_(
                &carol(),
                json!({"message": 99, "block": 9, "revision": "e41b07c", "lines": [9, 1]})
            )),
            [issue("/lines", "must have its first line at most its last")]
        );
        assert_eq!(
            vetoed(move_(
                &carol(),
                json!({"message": 99, "block": 9, "revision": "e41b07c", "lines": [1, 9]})
            )),
            ("no_message".to_owned(), json!({"message": 99}))
        );
        let gone = world.say(&alice(), "Gone.");
        world.ok(&alice(), "deleteMessage", json!({"message": gone}));
        assert_eq!(
            vetoed(move_(
                &carol(),
                json!({"message": gone, "block": 9, "revision": "e41b07c", "lines": [1, 9]})
            ))
            .0,
            "deleted"
        );
        let draft = world.ok(&alice(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        assert_eq!(
            vetoed(move_(
                &carol(),
                json!({"message": draft, "block": 9, "revision": "e41b07c", "lines": [1, 9]})
            ))
            .0,
            "composing"
        );
        assert_eq!(
            vetoed(move_(
                &carol(),
                json!({"message": id, "block": 9, "revision": "e41b07c", "lines": [1, 9]})
            )),
            (
                "not_author".to_owned(),
                json!({"message": id, "author": "alice"})
            )
        );
        assert_eq!(
            vetoed(move_(
                &alice(),
                json!({"message": id, "block": 2, "revision": "e41b07c", "lines": [1, 9]})
            )),
            (
                "no_block".to_owned(),
                json!({"message": id, "block": 2, "blocks": 2})
            )
        );
        assert_eq!(
            vetoed(move_(
                &alice(),
                json!({"message": id, "block": 1, "revision": "e41b07c", "lines": [1, 9]})
            )),
            (
                "not_code".to_owned(),
                json!({"message": id, "block": 1, "kind": "action"})
            )
        );
        assert_eq!(
            vetoed(move_(
                &alice(),
                json!({"message": id, "block": 0, "revision": "9c1f2e7", "lines": [40, 48]})
            )),
            (
                "anchor_unchanged".to_owned(),
                json!({"message": id, "block": 0, "revision": "9c1f2e7", "lines": [40, 48]})
            )
        );

        // The author moves it; the first move records the anchor before.
        let once = world.ok(
            &alice(),
            "moveAnchor",
            json!({"message": id, "block": 0, "revision": "9c1f2e7", "lines": [41, 49]}),
        );
        assert_eq!(
            once["blocks"][0],
            json!({"kind": "code", "repo": "github.com/acme/shop", "path": "src/orders.rs", "revision": "9c1f2e7", "lines": [41, 49],
                   "snippet": "fn place() {", "original": {"revision": "9c1f2e7", "lines": [40, 48]}, "moves": 1, "movedAt": 1000, "movedBy": "alice"})
        );
        // A holder of anchorPermission acting for Alice, then a moderator.
        world.set_clock(2000);
        let bot = anchor_bot().acting_for("alice");
        let twice = world.ok(
            &bot,
            "moveAnchor",
            json!({"message": id, "block": 0, "revision": "e41b07c", "lines": [44, 52]}),
        );
        assert_eq!(
            twice["blocks"][0],
            json!({"kind": "code", "repo": "github.com/acme/shop", "path": "src/orders.rs", "revision": "e41b07c", "lines": [44, 52],
                   "snippet": "fn place() {", "original": {"revision": "9c1f2e7", "lines": [40, 48]}, "moves": 2, "movedAt": 2000, "movedBy": "bot", "movedFor": "alice"})
        );
        // Not an edit: no editedAt, no edits, no listEdits item, the
        // counts as they were, the search index as it was.
        assert!(twice.get("editedAt").is_none());
        assert_eq!(twice["edits"], json!(0));
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": id}))["items"],
            json!([])
        );
        assert_eq!(world.fields(&alice())["messages"], json!(1));
        assert_eq!(
            hits(&world, &alice(), json!({"query": "loop"})),
            [json!(["Review", "r1", id])]
        );
        // The event holds the params and the record after.
        let event = world.events().pop().unwrap();
        assert_eq!(event.change["operation"], json!("moveAnchor"));
        assert_eq!(event.change["result"]["blocks"][0], twice["blocks"][0]);
        assert_eq!(event.for_subject.as_deref(), Some("alice"));

        // An edit that gives the block back as read keeps its moves;
        // listEdits keeps the moved block as it was.
        let mut as_read = code_block("e41b07c", [44, 52]);
        as_read["snippet"] = json!("fn place() {");
        let kept = world.ok(
            &alice(),
            "edit",
            json!({"message": id, "blocks": [as_read.clone(), {"kind": "action", "title": "Reviewed", "status": "failed"}]}),
        );
        assert_eq!(kept["blocks"][0], twice["blocks"][0]);
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": id}))["items"][0]["blocks"][0],
            twice["blocks"][0]
        );
        // One that changes its snippet drops them.
        let mut changed = as_read;
        changed["snippet"] = json!("fn place(order: Order) {");
        let dropped = world.ok(
            &alice(),
            "edit",
            json!({"message": id, "blocks": [changed.clone()]}),
        );
        assert_eq!(dropped["blocks"], json!([changed]));

        // Past editWindowMs an edit is refused, a move is not; a moderator
        // moves another's.
        world.set_clock(100_000);
        assert_eq!(
            vetoed(world.call(&alice(), "edit", json!({"message": id, "body": "Late."}))).0,
            "edit_window_passed"
        );
        let late = world.ok(
            &moderator(),
            "moveAnchor",
            json!({"message": id, "block": 0, "revision": "f00d123", "lines": [44, 52]}),
        );
        assert_eq!(
            (
                &late["blocks"][0]["original"],
                &late["blocks"][0]["moves"],
                &late["blocks"][0]["movedBy"]
            ),
            (
                &json!({"revision": "e41b07c", "lines": [44, 52]}),
                &json!(1),
                &json!("mod")
            )
        );
        // An interrupted message is moved as a posted one.
        world.engine.shutdown();
    }
}

#[test]
fn list_pinned_lists_pinned_messages_of_every_level_by_id() {
    for target in targets("comments_list_pinned") {
        let world = world(&target, json!({}));
        let one = world.say(&alice(), "One.");
        let two = world.reply(&bob(), one, "Two.");
        let three = world.say(&bob(), "Three.");
        let four = world.reply(&alice(), three, "Four.");
        for id in [four, one, two] {
            world.ok(&alice(), "pin", json!({ "message": id }));
        }
        let ids = |page: &Value| -> Vec<i64> {
            page["items"]
                .as_array()
                .unwrap()
                .iter()
                .map(|item| item["id"].as_i64().unwrap())
                .collect()
        };
        let all = world.ok(&alice(), "listPinned", json!({}));
        assert_eq!(ids(&all), [one, two, four]);
        assert_eq!(all["next"], Value::Null);
        assert_eq!(all["items"][2], world.message(four));
        let first = world.ok(&alice(), "listPinned", json!({"limit": 2}));
        assert_eq!(ids(&first), [one, two]);
        let next = world.ok(
            &alice(),
            "listPinned",
            json!({"limit": 2, "cursor": first["next"]}),
        );
        assert_eq!(ids(&next), [four]);
        assert_eq!(next["next"], Value::Null);
        world.ok(&bob(), "deleteMessage", json!({ "message": two }));
        assert_eq!(
            ids(&world.ok(&alice(), "listPinned", json!({}))),
            [one, four]
        );
        assert_eq!(
            invalid(world.call(&alice(), "listPinned", json!({"cursor": "nope"}))),
            [issue("/cursor", "is not a cursor this operation returned")]
        );
        let _ = three;
        world.engine.shutdown();
    }
}

/// The cursor of the page after `rank`, as page() writes it.
fn cursor(rank: i64) -> String {
    superschematic_engine::behaviors::paging::base64url_encode(format!("after:{rank}").as_bytes())
}

/// The hits of a search, each `[schema, instanceId, message]`.
fn hits(world: &World, who: &Principal, params: Value) -> Vec<Value> {
    world.ns_ok(who, "searchMessages", params)["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|item| json!([item["schema"], item["instanceId"], item["message"]]))
        .collect()
}

fn sorted_hits(world: &World, who: &Principal, params: Value) -> Vec<Value> {
    let mut found = hits(world, who, params);
    found.sort_by_key(Value::to_string);
    found
}

#[test]
fn search_messages_finds_the_standing_bodies_the_caller_may_read() {
    for target in targets("comments_search") {
        let world = world(&target, json!({"moderatePermission": "comments.moderate"}));
        let top = world.say(&alice(), "The walnut desk shipped.");
        let reply = world.reply(&bob(), top, "Walnut stain looks great.");
        let other = world.say(&carol(), "Oak table instead of walnut?");
        let interrupted = world.ok(&bob(), "begin", json!({}))["id"].as_i64().unwrap();
        world.ok(
            &bob(),
            "appendDraft",
            json!({"message": interrupted, "text": "walnut legs are"}),
        );
        world.ok(&bob(), "interrupt", json!({"message": interrupted}));
        let draft = world.ok(&carol(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        world.ok(
            &carol(),
            "appendDraft",
            json!({"message": draft, "text": "walnut draft"}),
        );
        let deleted = world.say(&carol(), "walnut deleted");
        world.ok(&carol(), "deleteMessage", json!({"message": deleted}));
        let edited = world.say(&alice(), "walnut before");
        world.ok(
            &alice(),
            "edit",
            json!({"message": edited, "body": "maple after"}),
        );
        let purged = world.say(&bob(), "walnut purged");
        world.ok(&moderator(), "purge", json!({"message": purged}));
        world
            .on(
                &alice(),
                "Review",
                "r2",
                "post",
                json!({"body": "walnut on r2"}),
            )
            .unwrap();
        world
            .on(
                &alice(),
                "Memo",
                "m1",
                "post",
                json!({"body": "walnut memo"}),
            )
            .unwrap();

        let r1 = |id: i64| json!(["Review", "r1", id]);
        assert_eq!(
            sorted_hits(
                &world,
                &alice(),
                json!({"query": "WALNUT", "schema": "Review", "instanceId": "r1"})
            ),
            {
                let mut want = vec![r1(top), r1(reply), r1(other), r1(interrupted)];
                want.sort_by_key(Value::to_string);
                want
            }
        );
        // The text before an edit is never found; the text after is.
        assert!(hits(&world, &alice(), json!({"query": "before"})).is_empty());
        assert_eq!(
            hits(&world, &alice(), json!({"query": "maple"})),
            [r1(edited)]
        );
        // Every schema the caller may read, unless narrowed.
        assert_eq!(hits(&world, &alice(), json!({"query": "walnut"})).len(), 6);
        assert_eq!(
            hits(
                &world,
                &alice(),
                json!({"query": "walnut", "schema": "Memo"})
            ),
            [json!(["Memo", "m1", 1])]
        );
        // thread holds the top-level message and its replies.
        assert_eq!(
            sorted_hits(
                &world,
                &alice(),
                json!({"query": "walnut", "schema": "Review", "instanceId": "r1", "thread": top})
            ),
            {
                let mut want = vec![r1(top), r1(reply)];
                want.sort_by_key(Value::to_string);
                want
            }
        );
        // Every word counts, in any order; the ninth does not.
        assert_eq!(
            hits(&world, &alice(), json!({"query": "shipped desk walnut"})),
            [r1(top)]
        );
        assert_eq!(
            hits(
                &world,
                &alice(),
                json!({"query": "the walnut desk shipped the the the the nowhere"})
            ),
            [r1(top)]
        );
        assert!(hits(
            &world,
            &alice(),
            json!({"query": "the walnut desk shipped the the the nowhere"})
        )
        .is_empty());

        // rita may read no Memo: it is never found, and naming it finds
        // nothing, as a schema that does not compose Comments does.
        let rita = named("rita");
        assert!(hits(&world, &rita, json!({"query": "memo"})).is_empty());
        assert!(hits(&world, &rita, json!({"query": "walnut", "schema": "Memo"})).is_empty());
        assert!(hits(
            &world,
            &alice(),
            json!({"query": "walnut", "schema": "Nothing"})
        )
        .is_empty());
        assert!(hits(
            &world,
            &alice(),
            json!({"query": "walnut", "schema": "Review", "instanceId": "r9"})
        )
        .is_empty());
        assert_eq!(hits(&world, &rita, json!({"query": "walnut"})).len(), 5);
        assert!(hits(&world, &named("outsider"), json!({"query": "walnut"})).is_empty());

        // The refusals, in order.
        let search = |params: Value| world.ns(&alice(), "searchMessages", params);
        assert_eq!(
            invalid(search(
                json!({"query": "x", "instanceId": "r1", "thread": 1, "cursor": "nope"})
            )),
            [issue("/instanceId", "needs schema")]
        );
        assert_eq!(
            invalid(search(json!({"query": "x", "thread": 1, "cursor": "nope"}))),
            [issue("/thread", "needs instanceId")]
        );
        assert_eq!(
            invalid(search(json!({"query": "x", "cursor": "nope"}))),
            [issue("/cursor", "is not a cursor this operation returned")]
        );

        // An instance's delete leaves nothing of it.
        world
            .engine
            .instances()
            .delete(&alice(), "Review", "r2", WriteOptions::default())
            .unwrap();
        assert!(hits(&world, &alice(), json!({"query": "r2"})).is_empty());
        assert_eq!(hits(&world, &alice(), json!({"query": "walnut"})).len(), 5);
        let left = support::count(
            &world.engine,
            "SELECT COUNT(*) FROM bhv_comments__comment_search WHERE id = 'r2'",
        );
        assert_eq!(left, 0);
        world.engine.shutdown();
    }
}

#[test]
fn a_query_is_plain_words_and_a_hit_says_who_wrote_it_and_where() {
    for target in targets("comments_search_items") {
        let world = world(&target, json!({}));
        let plain = world.say(&alice(), "and near the walnut desk");
        let service = Principal::service("reviewer-bot", &[], "sa@issuer").acting_for("alice");
        let written = world.post(
            &service,
            json!({"body": "Walnut desk: AND is a word here, near the end."}),
        );
        world.set_clock(5000);
        world.ok(
            &service,
            "edit",
            json!({"message": written, "body": "Walnut desk: AND is a word here, near the end!"}),
        );
        // Quotes, AND, * and NEAR are text: " and * hold no word, AND and
        // NEAR are words.
        let mut found = hits(&world, &alice(), json!({"query": "\" AND * NEAR walnut"}));
        found.sort_by_key(Value::to_string);
        assert_eq!(
            found,
            [
                json!(["Review", "r1", plain]),
                json!(["Review", "r1", written])
            ]
        );
        assert!(hits(&world, &alice(), json!({"query": "\"\" *"})).is_empty());

        let result = world.ns_ok(
            &alice(),
            "searchMessages",
            json!({"query": "AND is a word"}),
        );
        let item = &result["items"][0];
        let names: Vec<&String> = item.as_object().unwrap().keys().collect();
        assert_eq!(
            names,
            [
                "schema",
                "instanceId",
                "message",
                "author",
                "for",
                "via",
                "state",
                "postedAt",
                "editedAt",
                "rank",
                "snippet"
            ]
        );
        assert_eq!(
            (
                &item["author"],
                &item["for"],
                &item["via"],
                &item["state"],
                &item["postedAt"],
                &item["editedAt"],
                &item["rank"]
            ),
            (
                &json!("service:reviewer-bot"),
                &json!("alice"),
                &json!("reviewer-bot"),
                &json!("posted"),
                &json!(1000),
                &json!(5000),
                &json!(1)
            )
        );
        // The snippet's parts: the matches, and the text between.
        let parts = item["snippet"].as_array().unwrap();
        let matched: Vec<&str> = parts
            .iter()
            .filter(|part| part["match"] == json!(true))
            .map(|part| part["text"].as_str().unwrap())
            .collect();
        assert_eq!(matched, ["AND", "is", "a", "word"], "{parts:?}");
        let text: String = parts
            .iter()
            .map(|part| part["text"].as_str().unwrap())
            .collect();
        assert!(text.contains("AND is a word here"), "{text}");
        let thread_item = world.ns_ok(&alice(), "searchMessages", json!({"query": "desk"}));
        assert!(thread_item["items"][0].get("thread").is_none());
        world.engine.shutdown();
    }
}

#[test]
fn search_messages_pages_by_rank_and_reaches_its_first_thousand_hits() {
    for target in targets("comments_search_pages") {
        let world = world(&target, json!({}));
        let ids: Vec<i64> = (0..5)
            .map(|n| world.say(&alice(), &format!("walnut {}", "walnut ".repeat(4 - n))))
            .collect();
        // More walnuts rank first: the first message has five.
        let first = world.ns_ok(
            &alice(),
            "searchMessages",
            json!({"query": "walnut", "limit": 2}),
        );
        let ranks: Vec<Value> = first["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| json!([item["message"], item["rank"]]))
            .collect();
        assert_eq!(ranks, [json!([ids[0], 1]), json!([ids[1], 2])]);
        assert_eq!(first["next"], json!(cursor(2)));
        let second = world.ns_ok(
            &alice(),
            "searchMessages",
            json!({"query": "walnut", "limit": 2, "cursor": cursor(2)}),
        );
        let ranks: Vec<Value> = second["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| json!([item["message"], item["rank"]]))
            .collect();
        assert_eq!(ranks, [json!([ids[2], 3]), json!([ids[3], 4])]);
        let last = world.ns_ok(
            &alice(),
            "searchMessages",
            json!({"query": "walnut", "limit": 2, "cursor": second["next"]}),
        );
        assert_eq!(last["items"][0]["rank"], json!(5));
        assert_eq!(last["next"], Value::Null);
        // A search reaches its first 1000 hits: past them nothing.
        assert_eq!(
            world.ns_ok(
                &alice(),
                "searchMessages",
                json!({"query": "walnut", "cursor": cursor(1000)})
            ),
            json!({"items": [], "next": null})
        );
        world.engine.shutdown();
    }
}

#[test]
fn a_search_stops_at_its_thousandth_hit() {
    for target in targets("comments_search_thousand") {
        let world = world(&target, json!({}));
        // 1002 messages hold the word, written beside the operations so the
        // test stays quick: the index rows are what a search reads.
        seed_index(&world.engine, 1002);
        let page = world.ns_ok(
            &alice(),
            "searchMessages",
            json!({"query": "walnut", "limit": 5, "cursor": cursor(997)}),
        );
        let ranks: Vec<Value> = page["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| item["rank"].clone())
            .collect();
        assert_eq!(ranks, [json!(998), json!(999), json!(1000)]);
        assert_eq!(page["next"], Value::Null);
        world.engine.shutdown();
    }
}

// seed_index writes `count` standing messages on Review r1 and their index
// rows straight to the tables, as post would.
fn seed_index(engine: &Engine, count: i64) {
    engine
        .db()
        .write(superschematic_engine::storage::Lock::Catalog, |tx| {
            for message in 1..=count {
                tx.run(
                    "INSERT INTO bhv_comments__comment_messages (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at, posted_at, stand)
                     VALUES ('default', 'Review', 'r1', ?, NULL, NULL, 'alice', NULL, 'posted', 'walnut', '[]', '[]', 1000, NULL, 0, 0, NULL, NULL, NULL, 1000, ?)",
                    &[SqlValue::Integer(message), SqlValue::Integer(message)],
                )?;
                tx.run(
                    "INSERT INTO bhv_comments__comment_search (namespace, schema, id, message, thread_root) VALUES ('default', 'Review', 'r1', ?, ?)",
                    &[SqlValue::Integer(message), SqlValue::Integer(message)],
                )?;
                tx.run(
                    "INSERT INTO bhv_comments__comment_search_text (rowid, body) SELECT row, 'walnut' FROM bhv_comments__comment_search WHERE message = ?",
                    &[SqlValue::Integer(message)],
                )?;
            }
            Ok::<_, superschematic_engine::storage::Error>(())
        })
        .expect("seed");
}

// Comments as it stood at migration 4, with an operation that writes a
// message, an edit and a reaction straight to its tables as batches 1 to
// 3 did.
struct CommentsAtFour;

impl Behavior for CommentsAtFour {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Comments", "fields": [{"name": "messages"}], "operations": [
            {"name": "seed", "writes": true, "resultSchema": true, "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"],
                "properties": {"body": {"type": "string"}, "thread": {"type": "integer"}, "state": {"enum": ["posted", "interrupted", "composing", "deleted"]},
                               "pinned": {"type": "boolean"}, "edited": {"type": "boolean"}, "reacted": {"type": "boolean"}}}}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        let mut migrations = superschematic_engine_behaviors::Comments.hooks().migrations;
        migrations.truncate(4);
        assert_eq!(
            migrations
                .iter()
                .map(|step| step.version)
                .collect::<Vec<_>>(),
            [1, 2, 3, 4]
        );
        Hooks {
            migrations,
            operations: vec![("seed", |_, cx, params| {
                let key = [
                    SqlValue::from(cx.namespace()),
                    SqlValue::from(cx.schema()),
                    SqlValue::from(cx.id()),
                ];
                let conversations = cx.sql().table("comment_conversations")?;
                let last = cx
                    .sql()
                    .get(
                        format!("SELECT last_message FROM {conversations} WHERE namespace = ? AND schema = ? AND id = ?"),
                        &key,
                    )?
                    .map(|row| row.i64("last_message"))
                    .transpose()?;
                let id = last.unwrap_or(0) + 1;
                let thread = params.get("thread").and_then(Value::as_i64);
                let state = params["state"].as_str().unwrap_or("posted");
                let stands = state == "posted" || state == "interrupted";
                let composing = state == "composing";
                let flag = |name: &str| params[name].as_bool().unwrap_or(false);
                let messages = cx.sql().table("comment_messages")?;
                let now = SqlValue::Integer(cx.now());
                let mut values = key.to_vec();
                values.extend([
                    SqlValue::Integer(id),
                    thread.map_or(SqlValue::Null, SqlValue::Integer),
                    SqlValue::from(cx.principal().subject.as_str()),
                    SqlValue::from(state),
                    SqlValue::from(if stands {
                        params["body"].as_str().unwrap_or_default()
                    } else {
                        ""
                    }),
                    now.clone(),
                    if flag("edited") {
                        now.clone()
                    } else {
                        SqlValue::Null
                    },
                    SqlValue::Integer(i64::from(flag("edited"))),
                    SqlValue::Integer(i64::from(flag("pinned"))),
                    if stands { now.clone() } else { SqlValue::Null },
                    if composing {
                        SqlValue::Integer(0)
                    } else {
                        SqlValue::Null
                    },
                    if composing {
                        now.clone()
                    } else {
                        SqlValue::Null
                    },
                    if stands {
                        SqlValue::Integer(id)
                    } else {
                        SqlValue::Null
                    },
                ]);
                cx.sql().run(
                    format!(
                        "INSERT INTO {messages} (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at, posted_at, draft_length, last_append_at, stand)
                         VALUES (?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, '[]', '[]', ?, ?, ?, ?, NULL, NULL, NULL, ?, ?, ?, ?)"
                    ),
                    &values,
                )?;
                if flag("edited") {
                    let edits = cx.sql().table("comment_edits")?;
                    let mut values = key.to_vec();
                    values.extend([
                        SqlValue::Integer(id),
                        SqlValue::from("before"),
                        SqlValue::from(cx.principal().subject.as_str()),
                        SqlValue::Integer(cx.now()),
                    ]);
                    cx.sql().run(
                        format!("INSERT INTO {edits} (namespace, schema, id, message, edit, body, blocks, mentions, edited_by, edited_at) VALUES (?, ?, ?, ?, 1, ?, '[]', '[]', ?, ?)"),
                        &values,
                    )?;
                }
                if flag("reacted") {
                    let reactions = cx.sql().table("comment_reactions")?;
                    let mut values = key.to_vec();
                    values.extend([
                        SqlValue::Integer(id),
                        SqlValue::from(cx.principal().subject.as_str()),
                        SqlValue::Integer(id),
                        SqlValue::Integer(cx.now()),
                    ]);
                    cx.sql().run(
                        format!("INSERT INTO {reactions} (namespace, schema, id, message, emoji, subject, reaction, reacted_at) VALUES (?, ?, ?, ?, '+1', ?, ?, ?)"),
                        &values,
                    )?;
                }
                let open = i64::from(stands && thread.is_none());
                let stand = if stands { id } else { 0 };
                let mut values = key.to_vec();
                if last.is_none() {
                    values.extend([
                        SqlValue::Integer(id),
                        SqlValue::Integer(open),
                        SqlValue::Integer(stand),
                    ]);
                    cx.sql().run(
                        format!("INSERT INTO {conversations} (namespace, schema, id, last_message, last_reaction, last_message_at, open_threads, resolved_threads, last_stand) VALUES (?, ?, ?, ?, 0, NULL, ?, 0, ?)"),
                        &values,
                    )?;
                } else {
                    let mut params = vec![
                        SqlValue::Integer(id),
                        SqlValue::Integer(open),
                        SqlValue::Integer(stand),
                        SqlValue::Integer(stand),
                    ];
                    params.extend(key);
                    cx.sql().run(
                        format!("UPDATE {conversations} SET last_message = ?, open_threads = open_threads + ?, last_stand = CASE WHEN last_stand > ? THEN last_stand ELSE ? END WHERE namespace = ? AND schema = ? AND id = ?"),
                        &params,
                    )?;
                }
                let count = cx.columns().get()?.i64("count")?;
                cx.columns()
                    .set(&[("count", SqlValue::Integer(count + i64::from(stands)))])?;
                Ok(json!({ "id": id }))
            })],
            fields: vec![("messages", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("count")?)))
            })],
            ..Hooks::default()
        }
    }
}

#[test]
fn migrations_5_and_6_index_and_attribute_a_conversation_migration_4_holds() {
    for target in targets("comments_search_migrate") {
        let mut at_four = target.options(BehaviorSet::new().with(CommentsAtFour));
        at_four.clock = Clock::Manual(1000);
        let engine = Engine::open(at_four).expect("open at migration 4");
        publish(&engine, "Review", &json!({}));
        create(&engine, "Review", "r1");
        let seed = |who: &Principal, params: Value| {
            engine
                .instances()
                .invoke(
                    who,
                    "Review",
                    "r1",
                    "seed",
                    &params,
                    WriteOptions::default(),
                )
                .expect("seed");
        };
        seed(
            &alice(),
            json!({"body": "walnut one", "pinned": true, "edited": true, "reacted": true}),
        );
        seed(
            &bob(),
            json!({"body": "walnut draft", "state": "composing"}),
        );
        seed(
            &bob(),
            json!({"body": "walnut three", "thread": 1, "pinned": true}),
        );
        seed(&carol(), json!({"body": "walnut gone", "state": "deleted"}));
        seed(
            &carol(),
            json!({"body": "walnut five", "state": "interrupted"}),
        );
        drop(engine);

        let engine = target
            .open_core_with(|options| {
                options.clock = Clock::Manual(5000);
                options.policy = Some(Policy::allow_all());
            })
            .shared();
        let world = World {
            engine: std::sync::Arc::clone(&engine),
        };
        let mut found = hits(&world, &alice(), json!({"query": "walnut"}));
        found.sort_by_key(Value::to_string);
        assert_eq!(
            found,
            [
                json!(["Review", "r1", 1]),
                json!(["Review", "r1", 3]),
                json!(["Review", "r1", 5])
            ]
        );
        // thread_root is the thread, or the message when top-level.
        assert_eq!(
            hits(
                &world,
                &alice(),
                json!({"query": "walnut", "schema": "Review", "instanceId": "r1", "thread": 1})
            )
            .len(),
            2
        );
        let pinned: Vec<Value> = world.ok(&alice(), "listPinned", json!({}))["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| item["id"].clone())
            .collect();
        assert_eq!(pinned, [json!(1), json!(3)]);
        // No for anywhere.
        let one = world.message(1);
        assert!(one.get("for").is_none(), "{one}");
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": 1}))["items"][0],
            json!({"edit": 1, "body": "before", "blocks": [], "mentions": [], "by": "alice", "at": 1000})
        );
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": 1, "emoji": "+1"})
            )["items"],
            json!([{"subject": "alice", "at": 1000}])
        );
        // What stands later is indexed as it comes to stand.
        world.ok(
            &bob(),
            "finish",
            json!({"message": 2, "body": "walnut finished"}),
        );
        assert_eq!(
            hits(&world, &alice(), json!({"query": "finished"})),
            [json!(["Review", "r1", 2])]
        );
        let tables = support::tables_of(&engine, "bhv_comments__");
        for name in [
            "bhv_comments__comment_search",
            "bhv_comments__comment_search_text",
            "bhv_comments__comment_messages_pinned",
        ] {
            assert!(tables.contains(&name.to_owned()), "{name}: {tables:?}");
        }
        let ledger: Vec<i64> = target
            .db()
            .read(|tx| {
                tx.all("SELECT version FROM engine_migrations WHERE owner = 'Comments' ORDER BY version", &[])?
                    .iter()
                    .map(|row| row.i64("version"))
                    .collect::<superschematic_engine::storage::Result<Vec<_>>>()
            })
            .unwrap();
        assert_eq!(ledger, [1, 2, 3, 4, 5, 6]);
        let _ = agent;
        engine.shutdown();
    }
}

#[test]
fn diacritics_fold_where_unaccent_is_had() {
    for target in targets("comments_search_unaccent") {
        let world = world(&target, json!({}));
        let id = world.say(&alice(), "Meet at the Café Zoë");
        assert_eq!(
            hits(&world, &alice(), json!({"query": "cafe zoe"})),
            [json!(["Review", "r1", id])]
        );
        assert_eq!(
            hits(&world, &alice(), json!({"query": "CAFÉ"})),
            [json!(["Review", "r1", id])]
        );
        if target.dialect == Dialect::Postgres {
            let mapped = support::texts(
                &world.engine,
                "SELECT d.dictname::text FROM pg_catalog.pg_ts_config_map m
                   JOIN pg_catalog.pg_ts_config c ON c.oid = m.mapcfg
                   JOIN pg_catalog.pg_ts_dict d ON d.oid = m.mapdict
                  WHERE c.cfgname = 'bhv_comments__comment_search_text_config' AND m.maptokentype = 1
                    AND c.cfgnamespace = CAST(current_schema() AS regnamespace)
                  ORDER BY m.mapseqno",
                &[],
            );
            assert_eq!(mapped, ["unaccent", "simple"]);
        }
        world.engine.shutdown();
    }
}

// A Postgres with no unaccent the engine may have: a database of the
// test's own, whose engine schema a role owns that may create nothing else
// in it, so neither the extension nor its schema can be made.
#[cfg(feature = "postgres")]
mod without_unaccent {
    use super::*;

    struct OwnDatabase {
        admin_url: String,
        name: String,
        url: String,
        role: String,
    }

    impl OwnDatabase {
        fn create(admin_url: &str, password: &str) -> OwnDatabase {
            let name = support::unique_name("comments_plain");
            let role = support::unique_name("comments_role");
            let mut client =
                postgres::Client::connect(admin_url, postgres::NoTls).expect("connect");
            client
                .batch_execute(&format!("CREATE ROLE {role} LOGIN PASSWORD '{password}'"))
                .expect("the role");
            client
                .batch_execute(&format!("CREATE DATABASE {name}"))
                .expect("the database");
            let (base, query) = admin_url.split_once('?').unwrap_or((admin_url, ""));
            let host = &base[..base.rfind('/').expect("a database in the URL")];
            let url = if query.is_empty() {
                format!("{host}/{name}")
            } else {
                format!("{host}/{name}?{query}")
            };
            let mut db = postgres::Client::connect(&url, postgres::NoTls).expect("connect");
            db.batch_execute(&format!(
                "REVOKE CREATE ON SCHEMA public FROM PUBLIC;
                 CREATE SCHEMA engine AUTHORIZATION {role}"
            ))
            .expect("the schema");
            OwnDatabase {
                admin_url: admin_url.to_owned(),
                name,
                url,
                role,
            }
        }

        // The database's URL as the role.
        fn url_as_role(&self, password: &str) -> String {
            let (scheme, rest) = self.url.split_once("://").expect("a URL");
            let rest = match rest.split_once('@') {
                Some((_, host)) if !host.contains('@') => host,
                _ => rest,
            };
            format!("{scheme}://{}:{password}@{rest}", self.role)
        }
    }

    impl Drop for OwnDatabase {
        fn drop(&mut self) {
            if let Ok(mut client) = postgres::Client::connect(&self.admin_url, postgres::NoTls) {
                let _ = client.batch_execute(&format!(
                    "DROP DATABASE IF EXISTS {} WITH (FORCE)",
                    self.name
                ));
                let _ = client.batch_execute(&format!("DROP ROLE IF EXISTS {}", self.role));
            }
        }
    }

    #[test]
    fn an_engine_that_composes_comments_migrates_where_unaccent_cannot_be_had() {
        for target in targets("comments_search_no_unaccent") {
            if target.dialect != Dialect::Postgres {
                continue;
            }
            let password = "comments-plain-test";
            let database = OwnDatabase::create(target.url().expect("a URL"), password);
            let mut options = superschematic_engine::EngineOptions::new(
                superschematic_engine::Storage::Postgres {
                    url: database.url_as_role(password),
                    options: superschematic_engine::storage::PostgresOptions {
                        listen: false,
                        ..Default::default()
                    },
                },
            );
            options.policy = Some(Policy::allow_all());
            options.behaviors = superschematic_engine_behaviors::core();
            let engine = Engine::open(options).expect("open as the role").shared();
            // Comments' migrations run, migration 6 among them.
            publish(&engine, "Review", &json!({}));
            create(&engine, "Review", "r1");
            let world = World {
                engine: std::sync::Arc::clone(&engine),
            };
            let id = world.say(&alice(), "Meet at the Café Zoë");
            // Case folds; diacritics do not, with simple alone.
            assert_eq!(
                hits(&world, &alice(), json!({"query": "CAFÉ zoë"})),
                [json!(["Review", "r1", id])]
            );
            assert!(hits(&world, &alice(), json!({"query": "cafe"})).is_empty());
            let mapped = support::texts(
                &engine,
                "SELECT d.dictname::text FROM pg_catalog.pg_ts_config_map m
                   JOIN pg_catalog.pg_ts_config c ON c.oid = m.mapcfg
                   JOIN pg_catalog.pg_ts_dict d ON d.oid = m.mapdict
                  WHERE c.cfgname = 'bhv_comments__comment_search_text_config' AND m.maptokentype = 1
                  ORDER BY m.mapseqno",
                &[],
            );
            assert_eq!(mapped, ["simple"]);
            assert_eq!(
                support::count(
                    &engine,
                    "SELECT COUNT(*) FROM pg_catalog.pg_extension WHERE extname = 'unaccent'"
                ),
                0
            );
            // Search still needs it, and says so.
            let mut document = comments_support::schema_of("Note", &json!({}));
            document["types"]["Note"]["behaviors"] =
                json!([{"name": "Search", "config": {"fields": ["title"]}}]);
            engine
                .schemas()
                .define(
                    &alice(),
                    document,
                    superschematic_engine::DefineOptions::default(),
                )
                .expect("define");
            let refused = engine
                .schemas()
                .publish(
                    &alice(),
                    "Note",
                    superschematic_engine::SchemaTarget::default(),
                )
                .expect_err("Search needs unaccent");
            assert!(
                refused.to_string().contains("needs the unaccent extension"),
                "{refused}"
            );
            // The engine goes on.
            assert_eq!(hits(&world, &alice(), json!({"query": "zoë"})).len(), 1);
            engine.shutdown();
            drop(world);
            drop(engine);
            drop(database);
        }
    }
}
