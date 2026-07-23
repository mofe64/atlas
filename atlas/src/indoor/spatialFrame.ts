export const maximumSpatialPoints = 100_000;

export type SpatialPose = {
  captureNs: number;
  frameId: string;
  childFrameId: string;
  x: number;
  y: number;
  z: number;
  qw: number;
  qx: number;
  qy: number;
  qz: number;
};

export type SpatialCloudMetadata = {
  sourceId: string;
  streamEpoch: string;
  sequence: number;
  observedAtUnixMs: number;
  receivedAtUnixMs: number;
  captureNs: number;
  frameId: string;
  voxelSizeM: number;
  pointCount: number;
  pose?: SpatialPose;
};

export type SpatialCloudFrame = {
  metadata: SpatialCloudMetadata;
  positions: Float32Array;
};

const textDecoder = new TextDecoder();

export function decodeSpatialFrame(packet: ArrayBuffer): SpatialCloudFrame | undefined {
  if (packet.byteLength === 0) return undefined;
  if (packet.byteLength < 8) throw new Error("Spatial frame packet is truncated");
  const bytes = new Uint8Array(packet);
  if (String.fromCharCode(...bytes.subarray(0, 4)) !== "ATS1") {
    throw new Error("Spatial frame packet has an unsupported version");
  }
  const headerLength = new DataView(packet, 4, 4).getUint32(0, true);
  const payloadOffset = 8 + headerLength;
  if (headerLength === 0 || payloadOffset > packet.byteLength) {
    throw new Error("Spatial frame metadata is invalid");
  }
  const metadata = JSON.parse(
    textDecoder.decode(bytes.subarray(8, payloadOffset)),
  ) as SpatialCloudMetadata;
  if (
    !Number.isSafeInteger(metadata.sequence) || metadata.sequence <= 0 ||
    !Number.isInteger(metadata.pointCount) || metadata.pointCount <= 0 ||
    metadata.pointCount > maximumSpatialPoints ||
    packet.byteLength - payloadOffset !== metadata.pointCount * 12
  ) {
    throw new Error("Spatial frame is not a complete bounded XYZ cloud");
  }
  const alignedPayload = packet.slice(payloadOffset);
  const positions = new Float32Array(alignedPayload);
  for (let index = 0; index < positions.length; index += 1) {
    if (!Number.isFinite(positions[index])) {
      throw new Error("Spatial frame contains a non-finite coordinate");
    }
  }
  return { metadata, positions };
}
