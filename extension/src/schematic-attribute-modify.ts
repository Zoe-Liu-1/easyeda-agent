/// <reference types="@jlceda/pro-api-types" />
import { readResponseContext, mutationContextError } from './eda-context';
import { exactJSON } from './preserve-instance';
import { ActionError, ErrorCodes, type ActionResult } from './protocol';

type Attribute = Awaited<ReturnType<typeof eda.sch_PrimitiveAttribute.getAll>>[number];
type State = Record<string, unknown>;

const ALLOWED_PATCH = new Set(['rotation', 'valueVisible', 'keyVisible', 'value']);

// Apply only explicitly supported text-property changes. The caller supplies the
// complete original attribute state, and the handler verifies that every
// unrequested field remains byte-for-byte equal after a fresh read.
export async function modifySchematicAttributeProperties(
	payload: State,
	readState: (attribute: Attribute) => Promise<State>,
): Promise<ActionResult> {
	const refuse = (message: string): never => { throw new ActionError(ErrorCodes.PRECONDITION_REFUSED, message); };
	const keys = new Set(['projectUuid', 'documentUuid', 'primitiveId', 'expected', 'patch']);
	if (Object.keys(payload).some(key => !keys.has(key))) refuse('Attribute modify accepts only identity, expected state and patch.');
	for (const key of ['projectUuid', 'documentUuid', 'primitiveId']) {
		if (typeof payload[key] !== 'string' || !(payload[key] as string).trim()) refuse(`Attribute modify requires ${key}.`);
	}
	const expected = payload.expected as State;
	if (!expected || typeof expected !== 'object' || Array.isArray(expected) || expected.primitiveId !== payload.primitiveId
		|| typeof expected.ParentPrimitiveId !== 'string' || !expected.ParentPrimitiveId.trim()
		|| !['Name', 'Designator', 'Value'].includes(expected.Key as string)) refuse('Attribute modify requires the complete original Name/Designator/Value state.');
	const patch = payload.patch as State;
	if (!patch || typeof patch !== 'object' || Array.isArray(patch) || Object.keys(patch).length === 0) refuse('Attribute modify patch must be a non-empty object.');
	for (const key of Object.keys(patch)) if (!ALLOWED_PATCH.has(key)) refuse(`Attribute modify does not allow patch.${key}.`);
	if ('rotation' in patch && patch.rotation !== null && (typeof patch.rotation !== 'number' || !Number.isFinite(patch.rotation))) refuse('patch.rotation must be finite or null.');
	for (const key of ['valueVisible', 'keyVisible']) if (key in patch && patch[key] !== null && typeof patch[key] !== 'boolean') refuse(`patch.${key} must be boolean or null.`);
	if ('value' in patch && typeof patch.value !== 'string') refuse('patch.value must be a string.');

	const identityBefore = await readResponseContext();
	if (identityBefore.projectUuid !== payload.projectUuid || identityBefore.documentUuid !== payload.documentUuid
		|| identityBefore.documentType !== 'schematic' || !identityBefore.tabId) refuse('Attribute modify requires the exact active schematic context.');
	const id = payload.primitiveId as string;
	const attribute = await eda.sch_PrimitiveAttribute.get(id);
	if (!attribute || attribute.getState_PrimitiveId() !== id) refuse('Attribute modify target is missing or has a different ID.');
	const before = await readState(attribute!);
	if (exactJSON(before) !== exactJSON(expected)) refuse('Attribute modify original state changed or expected state is incomplete.');

	// Name attributes may belong to parts, wires, flags or ports. Verify the
	// parent exists through the two supported primitive stores without assuming
	// one component type.
	const parentID = expected.ParentPrimitiveId as string;
	let parentComponent: any;
	let parentWire: any;
	try { parentComponent = await eda.sch_PrimitiveComponent.get(parentID); }
	catch { parentComponent = undefined; }
	if (!parentComponent) {
		try { parentWire = await eda.sch_PrimitiveWire.get(parentID); }
		catch { parentWire = undefined; }
	}
	if ((!parentComponent || parentComponent.getState_PrimitiveId() !== parentID)
		&& (!parentWire || parentWire.getState_PrimitiveId() !== parentID)) refuse('Attribute modify parent is missing or has a different ID.');

	const prewrite = await readResponseContext();
	if (mutationContextError(identityBefore, prewrite) || !prewrite.tabId) refuse('Attribute modify context changed before write.');
	let attempted = false;
	const errors: string[] = [];
	const targetState = { ...before };
	for (const [key, value] of Object.entries(patch)) {
		const stateKey = key === 'rotation' ? 'Rotation'
			: key === 'valueVisible' ? 'ValueVisible'
				: key === 'keyVisible' ? 'KeyVisible'
					: 'Value';
		targetState[stateKey] = value;
	}
	if (exactJSON(before) !== exactJSON(targetState)) {
		attempted = true;
		try {
			const ack = await eda.sch_PrimitiveAttribute.modify(id, patch as any);
			if (!ack) errors.push('modify returned no attribute; the write may have applied');
		}
		catch (error) { errors.push(`modify failed; the write may have applied: ${String(error)}`); }
	}
	let after: State | undefined;
	try {
		const fresh = await eda.sch_PrimitiveAttribute.get(id);
		if (!fresh || fresh.getState_PrimitiveId() !== id) throw new Error('fresh attribute is missing or has a different ID');
		after = await readState(fresh);
		if (exactJSON(after) !== exactJSON(targetState)) errors.push('fresh attribute properties differ from requested result');
	}
	catch (error) { errors.push(`fresh read failed: ${String(error)}`); }
	const identityAfter = await readResponseContext();
	const drift = mutationContextError(identityBefore, identityAfter);
	if (drift || !identityAfter.tabId) errors.push(`attribute modify context changed: ${drift ?? 'missing tabId'}`);
	const verified = errors.length === 0;
	return {
		result: {
			scope: 'attribute-properties', primitiveId: id, requested: patch,
			before, after: after ?? null, identityBefore, identityAfter,
			attempted, changed: exactJSON(before) !== exactJSON(after), verified, partial: !verified,
			errors,
		},
		...(verified ? {} : { warnings: ['Attribute properties are not fully verified; retain this response and fresh state. Do not retry automatically.'] }),
	};
}
