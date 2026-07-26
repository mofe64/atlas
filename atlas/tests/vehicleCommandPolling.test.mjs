import assert from "node:assert/strict";
import test from "node:test";
import { pollVehicleCommand } from "../src/command/vehicleCommandPolling.ts";

const initialReceipt = {
  id: "command-1",
  commandType: "return_to_launch",
  status: "accepted",
};

function virtualClock(receiptAt) {
  let now = 0;
  return {
    now: () => now,
    wait: async (durationMs) => {
      now += durationMs;
    },
    readReceipt: async () => receiptAt(now),
    elapsed: () => now,
  };
}

test("returns success when PX4 acknowledges near the command deadline", async () => {
  const clock = virtualClock((now) => ({
    ...initialReceipt,
    status: now >= 14_800 ? "succeeded" : "executing",
    resultMessage: now >= 14_800 ? "RTL accepted" : null,
  }));

  const outcome = await pollVehicleCommand(initialReceipt, {
    timeoutMs: 15_000,
    requestedAtUnixMs: 0,
    readReceipt: clock.readReceipt,
    now: clock.now,
    wait: clock.wait,
  });

  assert.equal(outcome.kind, "success");
  assert.equal(outcome.receipt.resultMessage, "RTL accepted");
  assert.equal(clock.elapsed(), 14_800);
});

test("returns a terminal failure without exhausting the poll budget", async () => {
  const clock = virtualClock((now) => ({
    ...initialReceipt,
    status: now >= 800 ? "rejected" : "accepted",
    resultMessage: now >= 800 ? "PX4 rejected RTL" : null,
  }));

  const outcome = await pollVehicleCommand(initialReceipt, {
    timeoutMs: 15_000,
    requestedAtUnixMs: 0,
    readReceipt: clock.readReceipt,
    now: clock.now,
    wait: clock.wait,
  });

  assert.equal(outcome.kind, "failure");
  assert.equal(outcome.receipt.status, "rejected");
  assert.equal(clock.elapsed(), 800);
});

test("returns pending after the command deadline plus margin", async () => {
  const clock = virtualClock(() => ({
    ...initialReceipt,
    status: "executing",
  }));

  const outcome = await pollVehicleCommand(initialReceipt, {
    timeoutMs: 15_000,
    requestedAtUnixMs: 0,
    readReceipt: clock.readReceipt,
    now: clock.now,
    wait: clock.wait,
  });

  assert.equal(outcome.kind, "pending");
  assert.equal(outcome.receipt.status, "executing");
  assert.equal(clock.elapsed(), 16_500);
});
