//! The HTML implicit-submission rule, held structurally (D44, amended).
//!
//! Enter in a form's text field clicks the form's default button: the
//! first submit control in tree order whose form owner is the form. Every
//! form a page renders must have one, and it must submit the form as such:
//! no `name`, so no `_action`, and no `formaction`, `formmethod`,
//! `formenctype`, `formtarget`, `formnovalidate` or `disabled`, and no
//! `<fieldset disabled>` around it but in that fieldset's first `<legend>`
//! child, which disables it as its own `disabled` would. No form nests
//! another, whose start tag the HTML parser drops, and no control names
//! its form with `form=`, so tree order is the whole rule. A `<template>`'s
//! contents are a fragment of their own, out of the page's forms, and the
//! rule holds in it as it does in the page: a script that puts them into
//! the page puts in what the parser read.
//!
//! The file ships with neither the extension nor the crate it writes. The
//! extension's tests copy it into each crate they test as
//! `tests/support/enter.rs`; an app's tests copy it from the repository.
//! A test declares `#[path = "support/enter.rs"] mod enter;` and calls
//! `enter::check_page(uri, &headers, &body)` on every answer it reads, or
//! `enter::assert_enter_submits(uri, &html)` on a page it knows is HTML.
#![allow(dead_code)]

/// One tag of a page: its name, lower case, whether it ends an element,
/// its attributes, names lower case and a bare attribute's value empty,
/// and its source, for a failure's message.
struct Tag<'a> {
    name: String,
    end: bool,
    attrs: Vec<(String, String)>,
    source: &'a str,
}

impl Tag<'_> {
    fn attr(&self, name: &str) -> Option<&str> {
        self.attrs.iter().find(|(attr, _)| attr == name).map(|(_, value)| value.as_str())
    }
}

/// The elements whose text holds no tags: a `<` in it is text.
const RAW_TEXT: &[&str] = &["script", "style", "textarea", "title"];

/// The tags of `html` in order, start and end tags alike: comments,
/// doctypes and the text of script, style, textarea and title elements
/// skipped. Topcoat's markup quotes its attributes and escapes `<` in
/// text, so a `<` that a letter, or `/` and a letter, follows begins a tag.
fn tags(html: &str) -> Vec<Tag<'_>> {
    let bytes = html.as_bytes();
    let mut tags = Vec::new();
    let mut at = 0;
    while let Some(offset) = html[at..].find('<') {
        let start = at + offset;
        let rest = &html[start..];
        if rest.starts_with("<!--") {
            at = rest.find("-->").map_or(html.len(), |end| start + end + 3);
            continue;
        }
        if rest.starts_with("<!") || rest.starts_with("<?") {
            at = rest.find('>').map_or(html.len(), |end| start + end + 1);
            continue;
        }
        let end = rest.starts_with("</");
        let name_at = start + if end { 2 } else { 1 };
        if !bytes.get(name_at).is_some_and(u8::is_ascii_alphabetic) {
            at = start + 1;
            continue;
        }
        let mut i = name_at;
        while i < bytes.len() && !bytes[i].is_ascii_whitespace() && !matches!(bytes[i], b'>' | b'/') {
            i += 1;
        }
        let name = html[name_at..i].to_ascii_lowercase();
        let mut attrs = Vec::new();
        loop {
            while i < bytes.len() && (bytes[i].is_ascii_whitespace() || bytes[i] == b'/') {
                i += 1;
            }
            if i >= bytes.len() {
                break;
            }
            if bytes[i] == b'>' {
                i += 1;
                break;
            }
            let attr_at = i;
            i += 1;
            while i < bytes.len() && !bytes[i].is_ascii_whitespace() && !matches!(bytes[i], b'>' | b'/' | b'=') {
                i += 1;
            }
            let attr = html[attr_at..i].to_ascii_lowercase();
            while i < bytes.len() && bytes[i].is_ascii_whitespace() {
                i += 1;
            }
            let mut value = String::new();
            if bytes.get(i) == Some(&b'=') {
                i += 1;
                while i < bytes.len() && bytes[i].is_ascii_whitespace() {
                    i += 1;
                }
                let value_at = i;
                match bytes.get(i) {
                    Some(&quote @ (b'"' | b'\'')) => {
                        let close = html[i + 1..].find(quote as char).map_or(html.len(), |close| i + 1 + close);
                        value = html[value_at + 1..close].to_owned();
                        i = (close + 1).min(html.len());
                    }
                    _ => {
                        while i < bytes.len() && !bytes[i].is_ascii_whitespace() && bytes[i] != b'>' {
                            i += 1;
                        }
                        value = html[value_at..i].to_owned();
                    }
                }
            }
            attrs.push((attr, value));
        }
        at = i;
        if !end && RAW_TEXT.contains(&name.as_str()) {
            let close = format!("</{name}");
            at = html[at..].to_ascii_lowercase().find(&close).map_or(html.len(), |text| at + text);
        }
        tags.push(Tag {
            name,
            end,
            attrs,
            source: &html[start..i],
        });
    }
    tags
}

/// A submit control: a `<button>` whose type is not `button` or `reset`
/// (an absent or unknown type is `submit`), or an `<input>` whose type is
/// `submit` or `image`, case aside.
fn is_submit(tag: &Tag) -> bool {
    let kind = tag.attr("type").map(str::to_ascii_lowercase);
    match tag.name.as_str() {
        "button" => !matches!(kind.as_deref(), Some("button" | "reset")),
        "input" => matches!(kind.as_deref(), Some("submit" | "image")),
        _ => false,
    }
}

/// The attributes a default button must not have: each makes Enter do
/// other than submit the form as such, or nothing at all.
const NOT_DEFAULT: &[&str] = &[
    "name",
    "formaction",
    "formmethod",
    "formenctype",
    "formtarget",
    "formnovalidate",
    "disabled",
];

/// The elements that hold nothing and take no end tag.
const VOID: &[&str] = &[
    "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr",
];

/// An element open where the scan is, by its tag's index: for a fieldset,
/// whether a `<legend>` child has opened in it yet; for a legend, whether
/// it is its fieldset's first legend child.
struct Open {
    tag: usize,
    legend_seen: bool,
    first_legend: bool,
}

/// A tree the rule reads on its own: the page, or a `<template>`'s
/// contents. `form` is the form open in it, and `first` that form's first
/// submit control, with the disabled fieldset that disables it, if one
/// does.
#[derive(Default)]
struct Fragment {
    open: Vec<Open>,
    form: Option<usize>,
    first: Option<(usize, Option<usize>)>,
}

/// Panics, naming `page` and the form's start tag, when a form of `html`
/// breaks the rule: it nests another form, it has no submit control, or
/// its first one is not a default button; or a control names its form.
/// The forms of a `<template>`'s contents are read as their own fragment's.
pub fn assert_enter_submits(page: &str, html: &str) {
    let tags = tags(html);
    let mut fragments = vec![Fragment::default()];
    for (at, tag) in tags.iter().enumerate() {
        if tag.name == "template" {
            if !tag.end {
                fragments.push(Fragment::default());
            } else if fragments.len() > 1 {
                let fragment = fragments.pop().unwrap_or_default();
                finish(page, &tags, &fragment);
            }
            continue;
        }
        let fragment = fragments.last_mut().expect("the page's fragment");
        if tag.end {
            if tag.name == "form"
                && let Some(form) = fragment.form.take()
            {
                let first = fragment.first.take();
                check(
                    page,
                    &tags[form],
                    first.map(|(button, fieldset)| (&tags[button], fieldset.map(|at| &tags[at]))),
                );
            }
            // The end tag closes its element and any left open in it.
            if let Some(i) = fragment.open.iter().rposition(|open| tags[open.tag].name == tag.name) {
                fragment.open.truncate(i);
            }
            continue;
        }
        if tag.name == "form" {
            if let Some(form) = fragment.form {
                panic!(
                    "{page}: the form {} holds another, {}, whose start tag the HTML parser drops",
                    tags[form].source, tag.source
                );
            }
            fragment.form = Some(at);
        } else {
            if let Some(owner) = tag.attr("form") {
                panic!(
                    "{page}: {} names its form ({owner}), so its place in the tree does not say which form Enter submits",
                    tag.source
                );
            }
            if fragment.form.is_some() && fragment.first.is_none() && is_submit(tag) {
                fragment.first = Some((at, disabling_fieldset(&tags, &fragment.open)));
            }
        }
        if !VOID.contains(&tag.name.as_str()) {
            let mut first_legend = false;
            if tag.name == "legend"
                && let Some(parent) = fragment.open.last_mut()
                && tags[parent.tag].name == "fieldset"
                && !parent.legend_seen
            {
                parent.legend_seen = true;
                first_legend = true;
            }
            fragment.open.push(Open {
                tag: at,
                legend_seen: false,
                first_legend,
            });
        }
    }
    while let Some(fragment) = fragments.pop() {
        finish(page, &tags, &fragment);
    }
}

/// Panics when a fragment's end leaves a form open.
fn finish(page: &str, tags: &[Tag], fragment: &Fragment) {
    if let Some(form) = fragment.form {
        panic!("{page}: the form {} is not closed", tags[form].source);
    }
}

/// The fieldset of `open`, the elements a control sits in, that disables
/// it: one with `disabled` whose first legend child does not hold the
/// control. The legend is the fieldset's child, so it is the next element
/// open after it.
fn disabling_fieldset(tags: &[Tag], open: &[Open]) -> Option<usize> {
    open.iter()
        .enumerate()
        .find(|(i, element)| {
            let tag = &tags[element.tag];
            tag.name == "fieldset" && tag.attr("disabled").is_some() && !open.get(i + 1).is_some_and(|child| child.first_legend)
        })
        .map(|(_, element)| element.tag)
}

/// `assert_enter_submits` on an answer that is an HTML page, by its
/// `Content-Type`: any other answer, such as the JSON API's, is left alone.
pub fn check_page(page: &str, headers: &http::HeaderMap, body: &str) {
    let html = headers
        .get(http::header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|kind| kind.to_ascii_lowercase().starts_with("text/html"));
    if html {
        assert_enter_submits(page, body);
    }
}

/// Panics when `first`, the first submit control of `form`, is missing or
/// is not a default button: an attribute of its own, or the fieldset that
/// disables it.
fn check(page: &str, form: &Tag, first: Option<(&Tag, Option<&Tag>)>) {
    let Some((button, fieldset)) = first else {
        panic!("{page}: the form {} has no submit button for Enter to press", form.source);
    };
    if let Some(attr) = NOT_DEFAULT.iter().find(|attr| button.attr(attr).is_some()) {
        panic!(
            "{page}: Enter in the form {} presses its first submit control, {}, which has {attr}: the default button must submit the form as such",
            form.source, button.source
        );
    }
    if let Some(fieldset) = fieldset {
        panic!(
            "{page}: Enter in the form {} presses its first submit control, {}, which the fieldset {} disables: the default button must submit the form as such",
            form.source, button.source, fieldset.source
        );
    }
}

#[cfg(test)]
mod tests {
    use super::{assert_enter_submits, check_page};

    fn headers(content_type: &str) -> http::HeaderMap {
        let mut headers = http::HeaderMap::new();
        headers.insert(http::header::CONTENT_TYPE, content_type.parse().unwrap());
        headers
    }

    #[test]
    #[should_panic(expected = "has no submit button")]
    fn an_html_answer_is_held_to_the_rule() {
        check_page(
            "page",
            &headers("text/html; charset=utf-8"),
            r#"<form method="post"><input name="q"></form>"#,
        );
    }

    #[test]
    fn any_other_answer_is_left_alone() {
        check_page("api", &headers("application/json"), r#"{"html": "<form></form>"}"#);
        check_page("bare", &http::HeaderMap::new(), "<form></form>");
    }

    #[test]
    fn the_hidden_default_ahead_of_row_buttons_passes() {
        assert_enter_submits(
            "rows",
            r#"<form method="post"><button type="submit" class="ss-submit" hidden=""></button><input name="tags[0]" type="text"><button type="submit" class="ss-add" name="_action" value="add:tags" formnovalidate="">Add</button><button type="submit">Save</button></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "which has name")]
    fn a_row_button_ahead_of_the_default_fails() {
        assert_enter_submits(
            "rows",
            r#"<form method="post"><input name="tags[0]" type="text"><button type="submit" name="_action" value="add:tags" formnovalidate="">Add</button><button type="submit">Save</button></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "has no submit button")]
    fn a_form_without_a_submit_fails() {
        assert_enter_submits(
            "none",
            r#"<form method="post"><input name="q" type="text"><button type="button">Clear</button></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "holds another")]
    fn a_nested_form_fails() {
        assert_enter_submits(
            "nested",
            r#"<form method="post"><form method="get"><button type="submit">Go</button></form></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "which has name")]
    fn a_button_without_a_type_is_a_submit() {
        assert_enter_submits(
            "untyped",
            r#"<form method="post"><button name="_action" value="add:tags">Add</button><button type="submit">Save</button></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "names its form")]
    fn a_control_that_names_its_form_fails() {
        assert_enter_submits(
            "owner",
            r#"<form id="f" method="post"><button type="submit">Save</button></form><button type="submit" form="f">Elsewhere</button>"#,
        );
    }

    #[test]
    #[should_panic(expected = "which has disabled")]
    fn a_disabled_default_fails() {
        assert_enter_submits(
            "disabled",
            r#"<form method="post"><button type="submit" disabled="">Save</button></form>"#,
        );
    }

    #[test]
    fn a_templates_controls_are_not_the_forms() {
        assert_enter_submits(
            "template",
            concat!(
                r#"<form method="post"><template><button type="submit" name="_action" value="add:tags">Add</button></template>"#,
                r#"<button type="submit">Save</button></form>"#,
            ),
        );
    }

    #[test]
    fn a_form_in_a_template_in_a_form_is_not_nested() {
        assert_enter_submits(
            "template",
            concat!(
                r#"<form method="post"><template><form method="get"><button type="submit">Go</button></form></template>"#,
                r#"<button type="submit">Save</button></form>"#,
            ),
        );
    }

    #[test]
    #[should_panic(expected = "has no submit button")]
    fn a_form_in_a_template_is_held_to_the_rule() {
        assert_enter_submits(
            "template",
            r#"<template><form method="post"><input name="q" type="text"></form></template><form method="get"><button type="submit">Go</button></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "which has name")]
    fn a_row_button_ahead_of_the_default_in_a_template_fails() {
        assert_enter_submits(
            "template",
            r#"<template><form method="post"><button type="submit" name="_action" value="add:tags">Add</button><button type="submit">Save</button></form></template>"#,
        );
    }

    #[test]
    #[should_panic(expected = "is not closed")]
    fn a_form_a_template_leaves_open_fails() {
        assert_enter_submits(
            "template",
            r#"<template><form method="post"><button type="submit">Save</button></template>"#,
        );
    }

    #[test]
    #[should_panic(expected = "which the fieldset <fieldset disabled=\"\"> disables")]
    fn a_default_in_a_disabled_fieldset_fails() {
        assert_enter_submits(
            "fieldset",
            r#"<form method="post"><fieldset disabled=""><input name="q" type="text"><button type="submit">Save</button></fieldset></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "which the fieldset <fieldset class=\"outer\" disabled> disables")]
    fn an_inner_fieldset_leaves_an_outer_ones_disabled() {
        assert_enter_submits(
            "fieldset",
            r#"<form method="post"><fieldset class="outer" disabled><fieldset><button type="submit">Save</button></fieldset></fieldset></form>"#,
        );
    }

    #[test]
    fn a_default_in_a_disabled_fieldsets_first_legend_passes() {
        assert_enter_submits(
            "fieldset",
            r#"<form method="post"><fieldset disabled=""><legend>Who <button type="submit">Save</button></legend><input name="q" type="text"></fieldset></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "disables")]
    fn a_default_in_a_second_legend_fails() {
        assert_enter_submits(
            "fieldset",
            r#"<form method="post"><fieldset disabled=""><legend>Who</legend><legend><button type="submit">Save</button></legend></fieldset></form>"#,
        );
    }

    #[test]
    #[should_panic(expected = "disables")]
    fn a_legend_that_is_no_child_of_the_fieldset_fails() {
        assert_enter_submits(
            "fieldset",
            r#"<form method="post"><fieldset disabled=""><div><legend><button type="submit">Save</button></legend></div></fieldset></form>"#,
        );
    }

    #[test]
    fn a_disabled_fieldset_away_from_the_default_passes() {
        assert_enter_submits(
            "fieldset",
            concat!(
                r#"<form method="post"><fieldset disabled=""><input name="a" type="text"></fieldset>"#,
                r#"<button type="submit">Save</button><fieldset disabled><button type="submit">Other</button></fieldset></form>"#,
            ),
        );
    }

    #[test]
    fn markup_in_text_comments_and_quoted_values_is_not_a_control() {
        assert_enter_submits(
            "text",
            concat!(
                r#"<!doctype html><form method="get" data-note="a > b"><!-- <button name="x"> -->"#,
                r#"<script>if (a < b) { "<button name=x>" }</script><textarea name="t"><button name="x"></textarea>"#,
                r#"<button type="button">Preview</button><INPUT TYPE="SUBMIT" value="Go"></form>"#,
                r#"<button type="submit" name="outside">Outside every form</button>"#,
            ),
        );
    }
}
