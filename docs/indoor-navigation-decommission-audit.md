# Indoor Navigation Decommission — Remaining Work

Delete this file when the checklist is complete. Source, package, and current
documentation cleanup is complete; this file tracks only the retired
deployment still present on the live Pi.

- [ ] Build and install the current Agent and native Spatial Debian packages on
      the landed, disarmed Pi.
- [ ] Run `sudo atlas-setup`.
- [ ] Run `sudo atlas-setup doctor` and confirm the OAK depth stream,
      calibration, USB connection, Agent, and MAVSDK are healthy.
- [ ] Perform the native Spatial hardware smoke check.
- [ ] Remove the old `atlas-spatial-runtime` Docker container and image.
- [ ] Confirm no retired checkout-based Spatial environment or service remains.
- [ ] Delete this file.
