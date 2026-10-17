/**
 * The running action's deadline, so a wait inside a handler can use the time
 * the caller actually granted instead of a fixed guess. The FIFO queue runs one
 * head at a time and sets it around each run (action-queue.ts runHead).
 *
 * Why it exists (2026-10-17): the schematic readiness wait gave the host 5 s to
 * finish RealTimeSync. In a background tab the host's own sync is throttled and
 * routinely needs longer, so a healthy write failed with "bring the tab to the
 * foreground" although the request still had most of its budget left.
 */

let activeDeadlineAt: number | undefined;

/** Margin left for the handler's own reply after a wait. */
export const BUDGET_REPLY_MARGIN_MS = 1_500;

export function setActiveDeadline(at: number | undefined): void {
	activeDeadlineAt = at;
}

/** Milliseconds left before the running action's caller gives up, or undefined. */
export function remainingBudgetMs(nowMs: number = Date.now()): number | undefined {
	if (activeDeadlineAt === undefined) return undefined;
	return Math.max(0, activeDeadlineAt - nowMs - BUDGET_REPLY_MARGIN_MS);
}
