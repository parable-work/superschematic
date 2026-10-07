// A type's display (D48): the describe document carries it, the instance
// type's fields with their titles and icons and each operation's title;
// define and publish hold it to the type, its behaviors and its Workflow,
// with the compiler's wording, and the meta-schema holds its shape.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { SchemaDocumentError, type Engine } from '../dist/index.js';
import { alice, cleanup, clone, openTestEngine, schemaDocument, thrown, ticketsDocument } from './helpers.ts';

afterEach(cleanup);

type Doc = { types: Record<string, Record<string, unknown> & { display?: Record<string, unknown>; fields?: Array<Record<string, unknown>> }> };

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

// tickets returns the fixture with change applied to its Ticket type.
function tickets(change: (ticket: Doc['types'][string]) => void): Record<string, unknown> {
  const document = clone(ticketsDocument()) as Record<string, unknown> & Doc;
  change(document.types.Ticket);
  return document;
}

// refusals defines a document and returns the issues the engine refuses it with.
function refusals(document: Record<string, unknown>): Array<{ path: string; message: string }> {
  return thrown(() => openTestEngine().schemas.define(alice, document), SchemaDocumentError).issues;
}

describe('the describe document', () => {
  test("carries the instance type's display, its fields with their titles and icons, and each operation's title", () => {
    const engine = openTestEngine();
    publish(engine, ticketsDocument());
    const described = engine.tools.describe(alice, 'tickets');
    assert.deepEqual(described.display, {
      noun: 'Ticket',
      plural: 'Tickets',
      titleField: 'title',
      createLabel: 'New ticket',
      summaryFields: ['status', 'assignee'],
      states: {
        done: { label: 'Done', tone: 'success' },
        dropped: { label: 'Dropped', tone: 'danger' },
        implementing: { label: 'Implement', activeForm: 'Implementing', tone: 'active' },
        review: { label: 'Review', activeForm: 'In review', tone: 'warning' },
        todo: { label: 'To do', tone: 'muted' },
      },
      transitions: {
        implementing: { review: 'Send to review' },
        review: { done: 'Accept', implementing: 'Request changes' },
        todo: { dropped: 'Drop', implementing: 'Start' },
      },
    });
    assert.deepEqual(described.fields, [
      { name: 'title', title: 'Title', icon: 'text' },
      { name: 'assignee', title: 'Assignee', icon: 'person' },
      { name: 'body' },
    ]);
    assert.deepEqual(
      described.operations.map((operation) => [operation.name, operation.title]),
      [
        ['create', 'Create tickets'],
        ['get', 'Get tickets'],
        ['list', 'List tickets'],
        ['update', 'Update tickets'],
        ['delete', 'Delete tickets'],
        ['transition', 'tickets: transition'],
        ['comment', 'tickets: comment'],
        ['listComments', 'tickets: listComments'],
      ]
    );
    // Each title is its tool's.
    const titles = new Map(engine.tools.manifest(alice).tools.map((tool) => [tool.name, tool.title]));
    for (const operation of described.operations) {
      assert.equal(operation.title, titles.get(operation.tool), operation.name);
    }
  });

  test('names a field by its key in an instance, and has no display for a type without one', () => {
    const engine = openTestEngine();
    publish(
      engine,
      tickets((ticket) => {
        (ticket.fields as Array<Record<string, unknown>>)[0].jsonTag = 'headline';
        (ticket.display as Record<string, unknown>).summaryFields = ['title', 'status'];
      })
    );
    const described = engine.tools.describe(alice, 'tickets');
    assert.equal(described.display?.titleField, 'headline');
    assert.deepEqual(described.display?.summaryFields, ['headline', 'status']);
    assert.deepEqual(described.fields[0], { name: 'headline', title: 'Title', icon: 'text' });

    publish(engine, schemaDocument('Plain', [{ name: 'title', typeRef: { name: 'string' } }]));
    const plain = engine.tools.describe(alice, 'Plain');
    assert.equal('display' in plain, false);
    assert.deepEqual(plain.fields, [{ name: 'title' }]);
  });

  test('a new version may change the display of a schema with instances', () => {
    const engine = openTestEngine();
    publish(engine, ticketsDocument());
    engine.instances.create(alice, 'tickets', { title: 'First' }, { id: 't1' });
    publish(
      engine,
      tickets((ticket) => {
        ticket.display = { noun: 'Issue', titleField: 'body', states: { todo: { label: 'Backlog' } } };
      })
    );
    assert.equal(engine.schemas.live(alice, 'tickets')?.version, 2);
    assert.deepEqual(engine.tools.describe(alice, 'tickets').display, { noun: 'Issue', titleField: 'body', states: { todo: { label: 'Backlog' } } });
  });
});

describe('define holds a display to its type', () => {
  test("its title is one of the type's own single text fields, shown to a reader", () => {
    const path = '/types/Ticket/display/titleField';
    assert.deepEqual(
      refusals(tickets((ticket) => ((ticket.display as Record<string, unknown>).titleField = 'nope'))),
      [{ path, message: 'type Ticket: @display titleField "nope" is not a field of the type (fields: title, assignee, body)' }]
    );
    assert.deepEqual(
      refusals(tickets((ticket) => ((ticket.display as Record<string, unknown>).titleField = 'status'))),
      [{ path, message: `type Ticket: @display titleField "status" is a field behavior Workflow adds; a title is one of the type's own fields (fields: title, assignee, body)` }]
    );
    const typed = (typeRef: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
      tickets((ticket) => {
        ticket.fields?.push({ name: 'other', typeRef, ...extra });
        (ticket.display as Record<string, unknown>).titleField = 'other';
      });
    assert.deepEqual(refusals(typed({ name: 'string', isArray: true })), [
      {
        path,
        message:
          'type Ticket: @display titleField other has type string[]; a title is a single text value: a string, or a scalar whose values are strings',
      },
    ]);
    assert.match(refusals(typed({ name: 'number' }))[0].message, /titleField other has type number;/);
    assert.match(refusals(typed({ name: 'Generic.Int64' }))[0].message, /titleField other has type Generic\.Int64;/);
    // The catalog gives these the string primitive; their values are JSON.
    for (const name of ['Generic.JSON', 'Generic.StringMap', 'Embedding.Vector']) {
      assert.match(refusals(typed({ name }))[0].message, new RegExp(`titleField other has type ${name.replace('.', '\\.')};`));
    }
    assert.deepEqual(refusals(typed({ name: 'string' }, { secret: true })), [
      { path, message: 'type Ticket: @display titleField names field other, which is secret' },
    ]);
    assert.deepEqual(refusals(typed({ name: 'string' }, { uiHidden: true })), [
      { path, message: 'type Ticket: @display titleField names field other, which is @uiHidden' },
    ]);
    // A scalar whose values are strings is text.
    const engine = openTestEngine();
    engine.schemas.define(alice, typed({ name: 'Identity.Name' }));
    engine.schemas.define(alice, typed({ name: 'Text.Markdown' }));
  });

  test("its summary fields are the type's own fields or its behaviors'", () => {
    assert.deepEqual(
      refusals(tickets((ticket) => ((ticket.display as Record<string, unknown>).summaryFields = ['status', 'commentCount', 'ghost']))),
      [
        {
          path: '/types/Ticket/display/summaryFields/2',
          message: 'type Ticket: @display summaryFields lists "ghost", which is not a field of the type or of its behaviors (fields: title, assignee, body)',
        },
      ]
    );
  });

  test('its states and transitions are its Workflow', () => {
    assert.deepEqual(
      refusals(
        tickets((ticket) => {
          const display = ticket.display as { states: Record<string, unknown>; transitions: Record<string, Record<string, string>> };
          display.states.lost = { label: 'Lost' };
          display.transitions.done = { todo: 'Reopen' };
          display.transitions.todo.review = 'Skip ahead';
        })
      ),
      [
        {
          path: '/types/Ticket/display/states/lost',
          message: 'type Ticket: @display states labels "lost", which is not a state of its Workflow (todo, implementing, review, done, dropped)',
        },
        {
          path: '/types/Ticket/display/transitions/done/todo',
          message: 'type Ticket: @display transitions labels the move from done to todo, which is not a transition of its Workflow',
        },
        {
          path: '/types/Ticket/display/transitions/todo/review',
          message: 'type Ticket: @display transitions labels the move from todo to review, which is not a transition of its Workflow',
        },
      ]
    );
    assert.deepEqual(
      refusals(
        tickets((ticket) => {
          ticket.behaviors = [{ name: 'Comments' }];
          (ticket.display as Record<string, unknown>).summaryFields = ['assignee'];
        })
      ),
      [{ path: '/types/Ticket/display', message: "type Ticket: @display states and transitions label a Workflow's; the type does not compose Workflow" }]
    );
  });

  test('a nested type takes a display of its fields, and no states', () => {
    const nested = (display: Record<string, unknown>) =>
      schemaDocument('Order', [{ name: 'line', typeRef: { name: 'Line' } }], {
        types: { Line: { name: 'Line', role: 'EmbeddedStruct', fields: [{ name: 'sku', typeRef: { name: 'string' } }], display } },
      });
    openTestEngine().schemas.define(alice, nested({ noun: 'Line', titleField: 'sku', summaryFields: ['sku'] }));
    assert.deepEqual(refusals(nested({ states: { open: { label: 'Open' } } })), [
      { path: '/types/Line/display', message: "type Line: @display states and transitions label a Workflow's; the type does not compose Workflow" },
    ]);
  });

  test('the meta-schema holds its shape', () => {
    const shape = (display: unknown) => refusals(tickets((ticket) => (ticket.display = display as Record<string, unknown>)));
    for (const display of [
      {},
      { noun: ' ' },
      { states: { todo: { tone: 'blue' } } },
      { states: { todo: {} } },
      { states: { 'to do': { label: 'To do' } } },
      { transitions: { todo: {} } },
      { summaryFields: ['title', 'title'] },
      { summaryFields: [] },
      { colour: 'red' },
    ]) {
      const issues = shape(display);
      assert.ok(issues.length > 0, JSON.stringify(display));
      assert.ok(issues.every((issue) => issue.path.startsWith('/types/Ticket/display')), JSON.stringify(issues));
    }
  });
});
