from launch import LaunchDescription
from launch.actions import (
    DeclareLaunchArgument,
    GroupAction,
    IncludeLaunchDescription,
    Shutdown,
    TimerAction,
)
from launch.conditions import IfCondition
from launch.launch_description_sources import PythonLaunchDescriptionSource
from launch.substitutions import LaunchConfiguration, PathJoinSubstitution
from launch_ros.actions import Node, SetRemap
from launch_ros.substitutions import FindPackageShare


def generate_launch_description():
    # All vendor topic names and driver arguments are confined to this provider
    # adapter. Generic Atlas nodes consume only /atlas/spatial/*.
    device_id = LaunchConfiguration("device_id")
    vio_enabled = LaunchConfiguration("vio_enabled")
    stable_params_file = PathJoinSubstitution(
        [FindPackageShare("atlas_spatial_runtime"), "config", "depthai_rgbd_imu.yaml"]
    )
    external_vio_params_file = PathJoinSubstitution(
        [FindPackageShare("atlas_spatial_runtime"), "config", "rtabmap_vio.yaml"]
    )

    stable_driver = IncludeLaunchDescription(
        PythonLaunchDescriptionSource(
            PathJoinSubstitution(
                [FindPackageShare("depthai_ros_driver_v3"), "launch", "driver.launch.py"]
            )
        ),
        launch_arguments={
            "rs_compat": "true",
            # The upstream launch file accepts driver configuration through a
            # parameter file, not arbitrary dotted launch arguments. The file
            # uses $(var device_id), so keep this launch configuration in the
            # included context even though the upstream launch does not
            # declare a device_id argument of its own.
            "device_id": device_id,
            "params_file": stable_params_file,
            # The vendor calibration tree is provider-local and rooted under
            # Atlas's stable mount frame. It supplies measured camera/IMU
            # extrinsics to the external estimator; the versioned Atlas
            # transform bundle remains authoritative from body_frd outward.
            "publish_tf_from_calibration": "true",
            "parent_frame": "oak_mount",
            "depth_module.depth_profile": "640x400x20",
            "depth_module.infra_profile": "640x400x20",
            "rgb_camera.color_profile": "640x400x20",
            # The OAK-D Lite's mono pair is global-shutter. Publish its
            # rectified outputs for motion estimation; RGB-D remains available
            # independently for colour/depth health and cloud projection.
            "enable_infra1": "true",
            "enable_infra2": "true",
        }.items(),
    )
    remaps = [
        SetRemap(src="/camera/camera/color/image_raw", dst="/atlas/spatial/color/image_raw"),
        SetRemap(src="/camera/camera/color/camera_info", dst="/atlas/spatial/color/camera_info"),
        SetRemap(src="/camera/camera/depth/image_rect_raw", dst="/atlas/spatial/provider/depth_mm"),
        SetRemap(
            src="/camera/camera/depth/camera_info",
            dst="/atlas/spatial/provider/aligned_depth/camera_info",
        ),
        SetRemap(src="/camera/camera/imu", dst="/atlas/spatial/provider/imu/data_raw"),
        SetRemap(src="/camera/camera/imu/data", dst="/atlas/spatial/provider/imu/data_raw"),
        SetRemap(
            src="/camera/camera/infra1/image_rect_raw",
            dst="/atlas/spatial/provider/right/image_rect",
        ),
        SetRemap(
            src="/camera/camera/infra1/camera_info",
            dst="/atlas/spatial/provider/right/camera_info_depthai",
        ),
        SetRemap(
            src="/camera/camera/infra2/image_rect_raw",
            dst="/atlas/spatial/provider/left/image_rect",
        ),
        SetRemap(
            src="/camera/camera/infra2/camera_info",
            dst="/atlas/spatial/provider/left/camera_info_depthai",
        ),
        SetRemap(src="/camera/camera/vio/odometry", dst="/atlas/spatial/vio/odometry"),
    ]
    normalizer = Node(
        package="atlas_spatial_runtime",
        executable="atlas-spatial-depth-normalizer",
        name="atlas_spatial_depth_normalizer",
        output="screen",
        on_exit=Shutdown(reason="spatial depth normalizer exited"),
    )
    stereo_camera_info = Node(
        package="atlas_spatial_runtime",
        executable="atlas-spatial-stereo-camera-info",
        name="atlas_spatial_stereo_camera_info",
        output="screen",
        on_exit=Shutdown(reason="spatial stereo CameraInfo normalizer exited"),
    )
    imu_timestamp_gate = Node(
        package="atlas_spatial_runtime",
        executable="atlas-spatial-imu-timestamp-gate",
        name="atlas_spatial_imu_timestamp_gate",
        output="screen",
        on_exit=Shutdown(reason="spatial IMU timestamp gate exited"),
    )
    imu_orientation = Node(
        package="imu_filter_madgwick",
        executable="imu_filter_madgwick_node",
        name="atlas_spatial_imu_orientation",
        output="screen",
        parameters=[
            {
                "use_mag": False,
                "world_frame": "enu",
                "publish_tf": False,
            }
        ],
        remappings=[
            ("imu/data_raw", "/atlas/spatial/provider/imu/data_monotonic"),
            ("imu/data", "/atlas/spatial/imu/data"),
        ],
        on_exit=Shutdown(reason="spatial IMU orientation filter exited"),
    )
    external_vio = Node(
        package="rtabmap_odom",
        executable="stereo_odometry",
        name="atlas_spatial_stereo_inertial_odometry",
        output="screen",
        condition=IfCondition(vio_enabled),
        parameters=[external_vio_params_file],
        remappings=[
            ("left/image_rect", "/atlas/spatial/provider/left/image_rect"),
            ("left/camera_info", "/atlas/spatial/provider/left/camera_info"),
            ("right/image_rect", "/atlas/spatial/provider/right/image_rect"),
            ("right/camera_info", "/atlas/spatial/provider/right/camera_info"),
            ("imu", "/atlas/spatial/imu/data"),
            ("odom", "/atlas/spatial/vio/odometry"),
        ],
        on_exit=Shutdown(reason="spatial stereo-inertial odometry exited"),
    )
    return LaunchDescription(
        [
            DeclareLaunchArgument("device_id", default_value=""),
            DeclareLaunchArgument("vio_enabled", default_value="true", choices=["true", "false"]),
            GroupAction(
                [
                    *remaps,
                    stable_driver,
                    normalizer,
                    stereo_camera_info,
                    imu_timestamp_gate,
                    imu_orientation,
                    # DepthAI begins publishing during mono auto-exposure and
                    # calibration startup. Start RTAB-Map as a fresh process
                    # only after those provider-local streams have settled;
                    # reset_odom does not recreate all estimator state.
                    TimerAction(period=8.0, actions=[external_vio]),
                ]
            ),
        ]
    )
