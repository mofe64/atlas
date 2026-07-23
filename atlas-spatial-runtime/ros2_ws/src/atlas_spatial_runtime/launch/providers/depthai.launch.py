from launch import LaunchDescription
from launch.actions import DeclareLaunchArgument, GroupAction, IncludeLaunchDescription
from launch.conditions import IfCondition, UnlessCondition
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
    vio_params_file = PathJoinSubstitution(
        [FindPackageShare("atlas_spatial_runtime"), "config", "depthai_vio.yaml"]
    )

    def driver(params_file, condition):
        return IncludeLaunchDescription(
            PythonLaunchDescriptionSource(
                PathJoinSubstitution([FindPackageShare("depthai_ros_driver_v3"), "launch", "driver.launch.py"])
            ),
            condition=condition,
            launch_arguments={
                "rs_compat": "true",
                # The upstream launch file accepts driver configuration through a
                # parameter file, not arbitrary dotted launch arguments. The file
                # uses $(var device_id), so keep this launch configuration in the
                # included context even though the upstream launch does not
                # declare a device_id argument of its own.
                "device_id": device_id,
                "params_file": params_file,
                # Atlas owns transform semantics and provenance. Neither the
                # generic camera calibration tree nor VIO may publish TF here.
                "publish_tf_from_calibration": "false",
            }.items(),
        )

    stable_driver = driver(stable_params_file, UnlessCondition(vio_enabled))
    live_non_authoritative_vio_driver = driver(vio_params_file, IfCondition(vio_enabled))
    remaps = [
        SetRemap(src="/camera/camera/color/image_raw", dst="/atlas/spatial/color/image_raw"),
        SetRemap(src="/camera/camera/color/camera_info", dst="/atlas/spatial/color/camera_info"),
        SetRemap(src="/camera/camera/depth/image_rect_raw", dst="/atlas/spatial/provider/depth_mm"),
        SetRemap(src="/camera/camera/depth/camera_info", dst="/atlas/spatial/aligned_depth/camera_info"),
        SetRemap(src="/camera/camera/imu", dst="/atlas/spatial/imu/data"),
        SetRemap(src="/camera/camera/imu/data", dst="/atlas/spatial/imu/data"),
        SetRemap(src="/camera/camera/vio/odometry", dst="/atlas/spatial/vio/odometry"),
    ]
    normalizer = Node(
        package="atlas_spatial_runtime",
        executable="atlas-spatial-depth-normalizer",
        name="atlas_spatial_depth_normalizer",
        output="screen",
    )
    return LaunchDescription(
        [
            DeclareLaunchArgument("device_id", default_value=""),
            DeclareLaunchArgument("vio_enabled", default_value="true", choices=["true", "false"]),
            GroupAction([*remaps, stable_driver, live_non_authoritative_vio_driver, normalizer]),
        ]
    )
