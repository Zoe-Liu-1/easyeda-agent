/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

function fixture(mode = '') {
 const state: Record<string, any> = { primitiveId: 'attribute-1', X: 10, Y: 20, Rotation: 0, Color: null, FontName: null, FontSize: null, Bold: null, Italic: null, UnderLine: null, AlignMode: null, FillColor: null, Key: 'Name', Value: '={Manufacturer Part}', KeyVisible: false, ValueVisible: null, ParentPrimitiveId: 'part-1' };
 const expected = { ...state }; let reads = 0; let writes = 0; let document = 'page-1'; let project = 'project-1'; const patches: any[] = [];
 const attribute = () => Object.fromEntries(Object.keys(state).map(key => ['getState_' + (key === 'primitiveId' ? 'PrimitiveId' : key), () => mode === 'missing-getter-value' && key === 'ValueVisible' ? undefined : state[key]]));
 const g = globalThis as any; const previous = g.eda; const types = g.EDMT_EditorDocumentType;
 g.EDMT_EditorDocumentType = { HOME: -1, BLANK: 0, SCHEMATIC_PAGE: 1, PCB: 3 };
 g.eda = {
  dmt_Project: { getCurrentProjectInfo: async () => ({ uuid: project }) },
  dmt_SelectControl: { getCurrentDocumentInfo: async () => ({ uuid: document, tabId: 'tab-1', documentType: mode === 'wrong-document-type' ? 3 : 1 }) },
  sch_PrimitiveComponent: { get: async () => mode === 'wire-parent'
   ? undefined
   : { getState_PrimitiveId: () => mode === 'unsupported-parent' ? 'other-parent' : 'part-1', getState_ComponentType: () => mode === 'netflag-parent' ? 'netflag' : 'part' } },
  sch_PrimitiveWire: { get: async () => mode === 'wire-parent'
   ? { getState_PrimitiveId: () => 'part-1' }
   : undefined },
  sch_PrimitiveAttribute: {
   get: async () => { reads++; if (mode === 'before-context-drift') document = 'other-page'; if (mode === 'fresh-missing' && reads > 1) return undefined; return attribute(); },
   modify: async (id: string, patch: any) => {
    writes++; assert.equal(id, 'attribute-1'); patches.push(patch);
    if (mode !== 'ignored') { state.X = patch.x; state.Y = patch.y; }
    if (mode === 'text-changed') state.Value = 'changed';
    if (mode === 'visibility-changed') state.ValueVisible = true;
    if (mode === 'style-changed') state.FontSize = 12;
    if (mode === 'parent-changed') state.ParentPrimitiveId = 'part-2';
    if (mode === 'after-context-drift') project = 'other-project';
    if (mode === 'throw-after-write') throw new Error('SDK rejected after mutation');
    return mode === 'undefined-after-write' ? undefined : attribute();
   },
  },
 };
 return { expected, state, patches, reads: () => reads, writes: () => writes, payload: () => ({ projectUuid: 'project-1', documentUuid: 'page-1', primitiveId: 'attribute-1', expected, x: 30, y: 40 }), restore: () => { g.eda = previous; g.EDMT_EditorDocumentType = types; } };
}

test('attribute move sends only x/y and independently preserves formula, visibility and style', async () => {
 const f = fixture(); try {
  const r: any = (await runAction('schematic.attribute.move', f.payload())).result;
  assert.equal(r.verified, true); assert.equal(r.partial, false); assert.equal(r.preserved, true); assert.equal(r.attempted, true);
  assert.deepEqual(f.patches, [{ x: 30, y: 40 }]); assert.equal(f.reads(), 2); assert.deepEqual(r.before, f.expected); assert.deepEqual(r.after, { ...f.expected, X: 30, Y: 40 });
 } finally { f.restore(); }
});

test('attribute move accepts a bound Value attribute', async () => {
 const f = fixture(); try {
  f.state.Key = 'Value'; f.state.Value = '10k';
  const p = f.payload(); p.expected.Key = 'Value'; p.expected.Value = '10k';
  const r: any = (await runAction('schematic.attribute.move', p)).result;
  assert.equal(r.verified, true); assert.equal(r.after.Key, 'Value'); assert.equal(r.after.Value, '10k');
 } finally { f.restore(); }
});

test('attribute move accepts a netflag Name attribute', async () => {
 const f = fixture('netflag-parent'); try {
  const r: any = (await runAction('schematic.attribute.move', f.payload())).result;
  assert.equal(r.verified, true); assert.equal(r.attempted, true);
 } finally { f.restore(); }
});

test('attribute move accepts a native wire Name attribute', async () => {
 const f = fixture('wire-parent'); try {
  const r: any = (await runAction('schematic.attribute.move', f.payload())).result;
  assert.equal(r.verified, true); assert.equal(r.attempted, true);
 } finally { f.restore(); }
});

test('attribute move no-op still rereads, without invoking modify', async () => {
 const f = fixture(); try {
  const r: any = (await runAction('schematic.attribute.move', { ...f.payload(), x: 10, y: 20 })).result;
  assert.equal(r.verified, true); assert.equal(r.attempted, false); assert.equal(r.moved, false); assert.equal(f.writes(), 0); assert.equal(f.reads(), 2);
 } finally { f.restore(); }
});

for (const mode of ['wrong-document-type', 'before-context-drift', 'unsupported-parent', 'missing-getter-value']) {
 test(`attribute move fails closed before write: ${mode}`, async () => {
  const f = fixture(mode); try { await assert.rejects(() => runAction('schematic.attribute.move', f.payload())); assert.equal(f.writes(), 0); } finally { f.restore(); }
 });
}

for (const mode of ['ignored', 'text-changed', 'visibility-changed', 'style-changed', 'parent-changed', 'after-context-drift', 'fresh-missing', 'throw-after-write', 'undefined-after-write']) {
 test(`attribute move retains attempted and fresh evidence after ${mode}`, async () => {
  const f = fixture(mode); try {
   const r: any = (await runAction('schematic.attribute.move', f.payload())).result;
   assert.equal(r.verified, false); assert.equal(r.partial, true); assert.equal(r.attempted, true); assert.equal(r.preserved, false); assert.equal(f.writes(), 1); assert.ok(r.errors.length); assert.deepEqual(r.before, f.expected);
   if (mode === 'fresh-missing') assert.equal(r.after, null); else assert.equal(r.after.Value, f.state.Value);
  } finally { f.restore(); }
 });
}

for (const [name, mutate] of Object.entries<Record<string, any>>({
 'unknown-field': { valueVisible: false }, 'missing-x': { x: undefined }, 'null-x': { x: null }, 'nan-x': { x: NaN }, 'string-y': { y: '40' }, 'wrong-page': { documentUuid: 'other' },
})) {
 test(`attribute move rejects invalid payload ${name}`, async () => {
  const f = fixture(); try { await assert.rejects(() => runAction('schematic.attribute.move', { ...f.payload(), ...mutate })); assert.equal(f.writes(), 0); } finally { f.restore(); }
 });
}

for (const mode of ['stale-position', 'stale-formula', 'missing-state-field', 'unsupported-key']) {
 test(`attribute move rejects ${mode}`, async () => {
  const f = fixture(); try {
   const p = f.payload(); p.expected = { ...f.expected };
   if (mode === 'stale-position') p.expected.X = 99;
   if (mode === 'stale-formula') p.expected.Value = 'rendered name';
   if (mode === 'missing-state-field') delete p.expected.FillColor;
   if (mode === 'unsupported-key') p.expected.Key = 'Pin Name';
   await assert.rejects(() => runAction('schematic.attribute.move', p)); assert.equal(f.writes(), 0);
  } finally { f.restore(); }
 });
}
