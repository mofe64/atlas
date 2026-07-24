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

export type SpatialCloudUpdate = {
  metadata: SpatialCloudMetadata;
  pointCount: number;
};

const textDecoder = new TextDecoder();
const nativeLittleEndian = new Uint8Array(new Uint32Array([1]).buffer)[0] === 1;

export function decodeSpatialFrame(packet: ArrayBuffer): SpatialCloudFrame | undefined {
  const decoded = decodeSpatialPacket(packet);
  if (!decoded) return undefined;
  const positions = new Float32Array(decoded.metadata.pointCount * 3);
  copyAndValidatePositions(packet, decoded.payloadOffset, positions, positions.length);
  return { metadata: decoded.metadata, positions };
}

export function decodeSpatialFrameInto(
  packet: ArrayBuffer,
  positions: Float32Array,
): SpatialCloudUpdate | undefined {
  const decoded = decodeSpatialPacket(packet);
  if (!decoded) return undefined;
  const coordinateCount = decoded.metadata.pointCount * 3;
  if (positions.length < coordinateCount) {
    throw new Error("Spatial point buffer is smaller than the complete cloud");
  }
  copyAndValidatePositions(packet, decoded.payloadOffset, positions, coordinateCount);
  return { metadata: decoded.metadata, pointCount: decoded.metadata.pointCount };
}

function decodeSpatialPacket(packet: ArrayBuffer) {
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
  return { metadata, payloadOffset };
}

function copyAndValidatePositions(
  packet: ArrayBuffer,
  payloadOffset: number,
  positions: Float32Array,
  coordinateCount: number,
) {
  if (nativeLittleEndian) {
    new Uint8Array(
      positions.buffer,
      positions.byteOffset,
      coordinateCount * Float32Array.BYTES_PER_ELEMENT,
    ).set(new Uint8Array(
      packet,
      payloadOffset,
      coordinateCount * Float32Array.BYTES_PER_ELEMENT,
    ));
  } else {
    const source = new DataView(packet, payloadOffset);
    for (let index = 0; index < coordinateCount; index += 1) {
      positions[index] = source.getFloat32(index * Float32Array.BYTES_PER_ELEMENT, true);
    }
  }
  for (let index = 0; index < coordinateCount; index += 1) {
    if (!Number.isFinite(positions[index])) {
      throw new Error("Spatial frame contains a non-finite coordinate");
    }
  }
}
