/// <reference types="@jlceda/pro-api-types" />
import { ActionError, type ActionResult, ErrorCodes } from './protocol';
import { requireString, uint8ToBase64 } from './util';
import { sleep } from './deadlines';

type Payload = Record<string, unknown>;
const ARCHIVE_LIMIT = 16 * 1024 * 1024;
const pause = () => sleep(250);
function fail(message: string): never {
	throw new ActionError(ErrorCodes.EDA_CALL_FAILED, message);
}
function targetOf(payload: Payload): string {
	const target = requireString(payload, 'projectUuid');
	if (!target.trim()) throw new ActionError(ErrorCodes.MISSING_PAYLOAD_FIELD, 'projectUuid must not be blank.');
	return target;
}
async function assertProject(target: string) {
	const project = await eda.dmt_Project.getCurrentProjectInfo();
	if (!project || project.uuid !== target) fail('Active project does not match requested UUID; inspect state before retrying.');
	return project;
}

/** Project switching is destructive to unsaved data, even when called without the CLI. */
export async function projectOpen(payload: Payload): Promise<ActionResult> {
	const target = targetOf(payload);
	if (payload.allowDiscardUnsaved !== true) {
		throw new ActionError(ErrorCodes.PRECONDITION_REFUSED, 'Save all documents first and explicitly pass allowDiscardUnsaved:true.');
	}
	const pageUUID = payload.pageUuid === undefined ? undefined : requireString(payload, 'pageUuid');
	if (pageUUID !== undefined && !pageUUID.trim()) throw new ActionError(ErrorCodes.MISSING_PAYLOAD_FIELD, 'pageUuid must not be blank.');
	if (!await eda.dmt_Project.openProject(target)) fail('Official openProject returned false; inspect state before retrying.');
	let project;
	for (let i = 0; i < 40; i++) {
		project = await eda.dmt_Project.getCurrentProjectInfo();
		if (project?.uuid === target) break;
		if (i === 39) fail('Opened project identity did not settle; inspect state before retrying.');
		await pause();
	}
	let document;
	if (pageUUID) {
		let found = false;
		for (let i = 0; i < 40; i++) {
			await assertProject(target);
			const pages = await eda.dmt_Schematic.getAllSchematicPagesInfo();
			if (Array.isArray(pages) && pages.some(p => p.uuid === pageUUID)) { found = true; break; }
			await pause();
		}
		if (!found) fail('Project opened but requested schematic page is unavailable; do not repeat project creation.');
		await assertProject(target);
		if (!await eda.dmt_EditorControl.openDocument(pageUUID)) fail('Project opened but page open failed; inspect state before retrying.');
		for (let i = 0; i < 40; i++) {
			await assertProject(target);
			document = await eda.dmt_SelectControl.getCurrentDocumentInfo();
			if (document?.uuid === pageUUID && document?.parentProjectUuid === target) break;
			if (i === 39) fail('Project opened but page identity did not settle.');
			await pause();
		}
	}
	await assertProject(target);
	return { result: { uuid: target, friendlyName: project?.friendlyName, opened: true, verified: true,
		...(document ? { documentUuid: document.uuid, documentVerified: true } : {}) } };
}

const IMPORT_OPTIONS = ['ImportDocument', 'ImportDocumentExtractLibraries'];
const IMPORT_TIMEOUT_MS = 120000;

/**
 * Import a native project file as a NEW project (never into an existing one).
 * The official call is @beta and may wait on host UI, so a timeout leaves the
 * outcome unknown: the caller must list projects before any retry.
 */
export async function projectImport(payload: Payload): Promise<ActionResult> {
	const fileName = requireString(payload, 'fileName');
	const encoded = requireString(payload, 'base64');
	const friendlyName = requireString(payload, 'friendlyName');
	if (!fileName.trim() || !friendlyName.trim()) throw new ActionError(ErrorCodes.MISSING_PAYLOAD_FIELD, 'fileName and friendlyName must not be blank.');
	const importOption = payload.importOption === undefined ? 'ImportDocument' : requireString(payload, 'importOption');
	if (!IMPORT_OPTIONS.includes(importOption)) throw new ActionError(ErrorCodes.PRECONDITION_REFUSED, `importOption must be one of ${IMPORT_OPTIONS.join(', ')}.`);
	if (encoded.length > Math.ceil(ARCHIVE_LIMIT / 3) * 4) fail('Project file exceeds 16 MiB transport limit.');
	const bytes = Uint8Array.from(atob(encoded), c => c.charCodeAt(0));
	if (!bytes.length || bytes.length > ARCHIVE_LIMIT) fail('Project file empty or exceeds 16 MiB transport limit.');
	let teamUuid = payload.teamUuid === undefined ? undefined : requireString(payload, 'teamUuid');
	if (!teamUuid) teamUuid = (await eda.dmt_Project.getCurrentProjectInfo())?.teamUuid;
	if (!teamUuid) fail('No teamUuid given and the active project has none; pass teamUuid explicitly.');
	const file = new File([bytes], fileName);
	let timer: ReturnType<typeof setTimeout> | undefined;
	const deadline = new Promise<never>((_, reject) => {
		timer = setTimeout(() => reject(new ActionError(ErrorCodes.EDA_CALL_FAILED,
			'Project import did not settle; outcome unknown. List projects by friendlyName before retrying.')), IMPORT_TIMEOUT_MS);
	});
	let brief;
	try {
		brief = await Promise.race([eda.sys_FileManager.importProjectByProjectFile(file, 'EasyEDA Pro',
			{ importOption: importOption as ESYS_ImportProjectImportOption },
			{ operation: 'New Project', newProjectOwnerTeamUuid: teamUuid, newProjectFriendlyName: friendlyName }), deadline]);
	}
	finally { clearTimeout(timer); }
	if (!brief?.uuid) fail('Official project import returned no project; outcome unknown (issue #203). Run project find by friendlyName before retrying.');
	const info = await eda.dmt_Project.getProjectInfo(brief.uuid);
	if (!info || info.uuid !== brief.uuid) fail('Imported project is not readable by UUID; inspect project list before retrying.');
	return { result: { uuid: info.uuid, friendlyName: info.friendlyName, teamUuid: info.teamUuid, importOption,
		bytes: bytes.length, created: true, verified: true, contentVerified: false } };
}

/** Read-only export of the explicitly identified active project; no script input. */
export async function projectExport(payload: Payload): Promise<ActionResult> {
	const target = targetOf(payload);
	await assertProject(target);
	const file = await eda.sys_FileManager.getProjectFile('project.epro2', undefined, 'epro2');
	if (!file || typeof file.arrayBuffer !== 'function') fail('Official project export returned no file.');
	if (!file.size || file.size > ARCHIVE_LIMIT) fail('Project archive empty or exceeds 16 MiB transport limit.');
	const bytes = new Uint8Array(await file.arrayBuffer());
	if (!bytes.length || bytes.length > ARCHIVE_LIMIT || bytes.length !== file.size) fail('Project archive size changed while reading.');
	await assertProject(target);
	return { result: { uuid: target, format: 'epro2', size: bytes.length, base64: uint8ToBase64(bytes) } };
}
