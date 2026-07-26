export type VehicleCommandReceipt = {
  id: string;
  commandType: string;
  status: string;
  resultCode?: string | null;
  resultMessage?: string | null;
};

export type VehicleCommandOutcome =
  | { kind: "success"; receipt: VehicleCommandReceipt }
  | { kind: "failure"; receipt: VehicleCommandReceipt }
  | { kind: "pending"; receipt: VehicleCommandReceipt };

type VehicleCommandPollOptions = {
  timeoutMs: number;
  requestedAtUnixMs: number;
  readReceipt: (commandId: string) => Promise<VehicleCommandReceipt>;
  deadlineMarginMs?: number;
  pollIntervalMs?: number;
  now?: () => number;
  wait?: (durationMs: number) => Promise<void>;
};

const failedCommandStates = new Set([
  "failed",
  "rejected",
  "timed_out",
  "cancelled",
]);

export function classifyVehicleCommandReceipt(
  receipt: VehicleCommandReceipt,
): VehicleCommandOutcome {
  const status = receipt.status.toLowerCase();
  if (status === "succeeded") return { kind: "success", receipt };
  if (failedCommandStates.has(status)) return { kind: "failure", receipt };
  return { kind: "pending", receipt };
}

export async function pollVehicleCommand(
  initial: VehicleCommandReceipt,
  {
    timeoutMs,
    requestedAtUnixMs,
    readReceipt,
    deadlineMarginMs = 1_500,
    pollIntervalMs = 400,
    now = Date.now,
    wait = waitFor,
  }: VehicleCommandPollOptions,
): Promise<VehicleCommandOutcome> {
  const pollDeadlineAtUnixMs = requestedAtUnixMs + timeoutMs + deadlineMarginMs;
  let current = initial;

  while (true) {
    const outcome = classifyVehicleCommandReceipt(current);
    if (outcome.kind !== "pending") return outcome;

    const remainingMs = pollDeadlineAtUnixMs - now();
    if (remainingMs <= 0) return outcome;

    await wait(Math.min(pollIntervalMs, remainingMs));
    current = await readReceipt(initial.id);
  }
}

function waitFor(durationMs: number) {
  return new Promise<void>((resolve) => globalThis.setTimeout(resolve, durationMs));
}
