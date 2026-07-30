# Indoor Navigation Decommission: Remaining Work

Use this checklist to remove the retired indoor-navigation deployment from the
live Raspberry Pi. Source, package, and current documentation cleanup is
complete.

**Warning:** Keep the aircraft landed, disarmed, and without propellers during
package and service changes.

- [ ] Build and install the current Agent and native Spatial Debian packages on
      the landed, disarmed Pi.
- [ ] Run `sudo atlas-setup`.
- [ ] Run `sudo atlas-setup doctor` and confirm the OAK depth stream,
      calibration, USB connection, Agent, and MAVSDK are healthy.
- [ ] Perform the native Spatial hardware smoke check.
- [ ] Remove the old `atlas-spatial-runtime` Docker container and image.
- [ ] Confirm no retired checkout-based Spatial environment or service remains.
- [ ] Delete this checklist after all checks are complete.
