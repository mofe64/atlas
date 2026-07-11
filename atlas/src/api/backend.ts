export type BackendHealth = {
  service: string;
  status: "ok";
};

export const backendBaseUrl = (
  import.meta.env.VITE_ATLAS_API_URL ?? "http://127.0.0.1:8080"
).replace(/\/$/, "");

export async function getBackendHealth(signal?: AbortSignal): Promise<BackendHealth> {
  const response = await fetch(`${backendBaseUrl}/healthz`, { signal });

  if (!response.ok) {
    throw new Error(`Atlas API returned HTTP ${response.status}`);
  }

  return response.json() as Promise<BackendHealth>;
}
