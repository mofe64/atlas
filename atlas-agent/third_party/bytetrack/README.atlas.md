# FoundationVision ByteTrack in Atlas

This directory vendors the C++ tracker core from the MIT-licensed
[FoundationVision/ByteTrack](https://github.com/FoundationVision/ByteTrack)
repository at commit `d1bf0191adff59bc8fcfeaa0b33d3d1642552a99`.

The upstream source files came from `deploy/ncnn/cpp/include` and
`deploy/ncnn/cpp/src`. The upstream MIT license is preserved in `LICENSE`.

Atlas-specific deployment adaptations are intentionally narrow:

- removed OpenCV rectangle/color types; the tracker itself only needs numeric boxes;
- exposed the upstream thresholds as constructor arguments while retaining the
  upstream defaults (`0.5`, `0.6`, `0.8`);
- carry the current detection index through `STrack` so Agent can attach the
  association to the correct normalized detection;
- reset the backend-local ID counter when Agent starts a new tracking session;
- added a versioned stdin/stdout worker protocol and one tracker instance per
  detector class to prevent cross-class association;
- added an optional Atlas CMC state warp after Kalman prediction and before IoU
  association. The worker converts Atlas's normalized previous-to-current
  homography into pixel coordinates and propagates the track covariance through
  the warp. Plain `byte_track` never applies this extension.

Atlas Agent remains authoritative for operator-visible, session-scoped track
IDs and resets on source, stream, model, dimension, or timestamp discontinuity.
The worker's integer IDs never leave that boundary directly.

Build with CMake and Eigen 3 headers:

```sh
cmake -S third_party/bytetrack -B /tmp/atlas-bytetrack-build -DCMAKE_BUILD_TYPE=Release
cmake --build /tmp/atlas-bytetrack-build --parallel
```
