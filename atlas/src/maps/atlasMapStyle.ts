import type { StyleSpecification } from "maplibre-gl";
import { terrainSource, type TerrainSource } from "../missions/terrain";

export const ATLAS_MAP_INITIAL_CENTER: [number, number] = [-0.1278, 51.5074];

const DEFAULT_TILE_URL = "https://tile.openstreetmap.org/{z}/{x}/{y}.png";

export function atlasMapStyle(dem: TerrainSource = terrainSource()): StyleSpecification {
  const tileUrl = import.meta.env.VITE_ATLAS_MAP_TILE_URL || DEFAULT_TILE_URL;
  return {
    version: 8,
    sources: {
      "atlas-basemap": {
        type: "raster",
        tiles: [tileUrl],
        tileSize: 256,
        maxzoom: 19,
        attribution: '© <a href="https://www.openstreetmap.org/copyright" target="_blank">OpenStreetMap contributors</a>',
      },
      "atlas-terrain": {
        type: "raster-dem",
        tiles: [dem.tileTemplate],
        tileSize: dem.tileSize,
        maxzoom: dem.zoom,
        encoding: dem.encoding,
        attribution: dem.attribution,
      },
      "atlas-hillshade": {
        type: "raster-dem",
        tiles: [dem.tileTemplate],
        tileSize: dem.tileSize,
        maxzoom: dem.zoom,
        encoding: dem.encoding,
      },
    },
    terrain: { source: "atlas-terrain", exaggeration: 1 },
    layers: [
      { id: "atlas-basemap", type: "raster", source: "atlas-basemap" },
      {
        id: "atlas-terrain-hillshade",
        type: "hillshade",
        source: "atlas-hillshade",
        paint: {
          "hillshade-shadow-color": "#28352b",
          "hillshade-highlight-color": "#f4f0e5",
          "hillshade-accent-color": "#6e7e68",
          "hillshade-exaggeration": 0.22,
        },
      },
    ],
  };
}
