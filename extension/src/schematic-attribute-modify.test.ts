/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

function fixture(mode = '') {
 const state: Record<string, any> = {
  primitiveId: 'attr-1', X: 10, Y: 20, Rotation: 90, Color: null, FontName: null,
  FontSize: null, Bold: null, Italic: null, UnderLine: null, AlignMode: null,
  FillColor: null, Key: 'Name', Value: 'NET_A', KeyVisible: false, ValueVisible: false,
  ParentPrimitiveId: 'wire-1',
 };
 const expected = { ...state };
 let writes = 0, reads = 0;
 const attribute = () => Object.fromEntries(Object.keys(state).map(key =>
  ['getState_' + (key === 'primitiveId' ? 'PrimitiveId' : key), () => state[key]]));
 const g = globalThis as any;
 const previous = g.eda;
 const types = g.EDMT_EditorDocumentType;
 g.EDMT_EditorDocumentType = { HOME: -1, BLANK: 0, SCHEMATIC_PAGE: 1, PCB: 3 };
 g.eda = {
  dmt_Project: { getCurrentProjectInfo: async () => ({ uuid: 'project-1' }) },
  dmt_SelectControl: { getCurrentDocumentInfo: async () => ({ uuid: 'page-1', tabId: 'tab-1', documentType: 1 }) },
  sch_PrimitiveComponent: { get: async () => undefined },
  sch_PrimitiveWire: { get: async (id: string) => ({ getState_PrimitiveId: () => id }) },
  sch_PrimitiveAttribute: {
   get: async () => { reads++; return attribute(); },
   modify: async (_id: string, patch: any) => {
    writes++;
    if (mode !== 'ignored') {
     if ('rotation' in patch) state.Rotation = patch.rotation;
     if ('valueVisible' in patch) state.ValueVisible = patch.valueVisible;
     if ('keyVisible' in patch) state.KeyVisible = patch.keyVisible;
     if ('value' in patch) state.Value = patch.value;
    }
    if (mode === 'visibility-dropped') state.ValueVisible = false;
    return attribute();
   },
  },
 };
 return {
  expected, writes: () => writes, reads: () => reads,
  payload: (patch: any = { rotation: 0, valueVisible: true }) => ({
   projectUuid: 'project-1', documentUuid: 'page-1', primitiveId: 'attr-1', expected, patch,
  }),
  restore: () => { g.eda = previous; g.EDMT_EditorDocumentType = types; },
 };
}

test('attribute modify rotates a wire Name and restores visibility with fresh verification', async () => {
 const f = fixture();
 try {
  const r: any = (await runAction('schematic.attribute.modify', f.payload())).result;
  assert.equal(r.verified, true);
  assert.equal(r.partial, false);
  assert.equal(r.after.Rotation, 0);
  assert.equal(r.after.ValueVisible, true);
  assert.equal(r.after.Value, 'NET_A');
  assert.equal(f.writes(), 1);
  assert.equal(f.reads(), 2);
 } finally { f.restore(); }
});

test('attribute modify reports a dropped visibility update as partial', async () => {
 const f = fixture('visibility-dropped');
 try {
  const r: any = (await runAction('schematic.attribute.modify', f.payload())).result;
  assert.equal(r.verified, false);
  assert.equal(r.partial, true);
  assert.equal(r.attempted, true);
  assert.equal(r.after.ValueVisible, false);
 } finally { f.restore(); }
});

test('attribute modify rejects unsupported patch before mutation', async () => {
 const f = fixture();
 try {
  await assert.rejects(() => runAction('schematic.attribute.modify', f.payload({ fontSize: 99 })));
  assert.equal(f.writes(), 0);
 } finally { f.restore(); }
});
