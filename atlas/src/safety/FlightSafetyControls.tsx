import { useEffect, useState } from "react";
import "./FlightSafetyControls.css";

export type FlightSafetyAction = "hold" | "return_to_launch" | "land";

export type FlightSafetyNotice = {
  tone: "neutral" | "nominal" | "caution" | "critical";
  message: string;
  receipt?: {
    id: string;
    status: string;
    commandType?: string;
  };
};

type FlightSafetyControlsProps = {
  aircraftName?: string;
  aircraftId?: string;
  availabilityMessage: string;
  pendingAction?: FlightSafetyAction;
  notice?: FlightSafetyNotice;
  holdDisabled?: boolean;
  returnHomeDisabled?: boolean;
  landDisabled?: boolean;
  onAction: (action: FlightSafetyAction) => void;
  onRefreshReceipt?: () => void;
};

export function FlightSafetyControls({
  aircraftName,
  aircraftId,
  availabilityMessage,
  pendingAction,
  notice,
  holdDisabled,
  returnHomeDisabled,
  landDisabled,
  onAction,
  onRefreshReceipt,
}: FlightSafetyControlsProps) {
  const [confirming, setConfirming] = useState<Exclude<FlightSafetyAction, "hold">>();

  useEffect(() => {
    setConfirming(undefined);
  }, [aircraftId]);

  function request(action: FlightSafetyAction) {
    if (action === "hold") {
      onAction(action);
      return;
    }
    setConfirming(action);
  }

  function confirm() {
    if (!confirming) return;
    const action = confirming;
    setConfirming(undefined);
    onAction(action);
  }

  const name = aircraftName || "this aircraft";
  const pendingLabel = pendingAction ? safetyActionLabel(pendingAction).toLowerCase() : undefined;

  return (
    <section className="flight-safety" aria-label="Flight safety">
      <header>
        <strong>Flight safety</strong>
        {aircraftId && <span>{shortIdentifier(aircraftId)}</span>}
      </header>

      <div className="flight-safety__actions">
        <button type="button" disabled={holdDisabled || Boolean(pendingAction)} onClick={() => request("hold")}>Hold</button>
        <button
          type="button"
          disabled={returnHomeDisabled || Boolean(pendingAction)}
          aria-expanded={confirming === "return_to_launch"}
          onClick={() => request("return_to_launch")}
        >
          Return home
        </button>
        <button
          type="button"
          className="flight-safety__land"
          disabled={landDisabled || Boolean(pendingAction)}
          aria-expanded={confirming === "land"}
          onClick={() => request("land")}
        >
          Land
        </button>
      </div>

      {confirming && (
        <div className={`flight-safety__confirmation flight-safety__confirmation--${confirming === "land" ? "critical" : "caution"}`}>
          <strong>{confirming === "land" ? `Land ${name}?` : `Return ${name} home?`}</strong>
          <p>{confirming === "land"
            ? "PX4 will descend at the aircraft’s current position. Continue only when the landing area is clear."
            : "PX4 will leave its current task and fly to its configured home position."}</p>
          <div>
            <button type="button" onClick={() => setConfirming(undefined)}>{confirming === "land" ? "Keep flying" : "Keep current flight"}</button>
            <button type="button" className={confirming === "land" ? "flight-safety__confirm-land" : undefined} onClick={confirm}>
              {confirming === "land" ? "Confirm land" : "Confirm return home"}
            </button>
          </div>
        </div>
      )}

      <div className={`flight-safety__receipt${notice ? ` flight-safety__receipt--${notice.tone}` : ""}`} role={notice?.tone === "critical" ? "alert" : "status"}>
        <span>{pendingLabel ? `Sending ${pendingLabel}…` : notice?.message || availabilityMessage}</span>
        {notice?.receipt && <small>Receipt {shortIdentifier(notice.receipt.id)} · {humanize(notice.receipt.status)}</small>}
        {notice?.tone === "caution" && notice.receipt && onRefreshReceipt && (
          <button type="button" disabled={Boolean(pendingAction)} onClick={onRefreshReceipt}>
            {pendingAction ? "Refreshing…" : "Refresh receipt"}
          </button>
        )}
      </div>
    </section>
  );
}

function safetyActionLabel(action: FlightSafetyAction) {
  if (action === "return_to_launch") return "Return home";
  return action === "hold" ? "Hold" : "Land";
}

function shortIdentifier(value: string) {
  return value.length > 8 ? `${value.slice(0, 8).toLowerCase()}…` : value.toLowerCase();
}

function humanize(value: string) {
  return value.toLowerCase().replace(/_/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}
