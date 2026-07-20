from launch import LaunchDescription
from launch.actions import DeclareLaunchArgument, GroupAction, IncludeLaunchDescription
from launch.launch_description_sources import PythonLaunchDescriptionSource
from launch.substitutions import LaunchConfiguration, PathJoinSubstitution
from launch_ros.actions import Node, SetRemap
from launch_ros.substitutions import FindPackageShare


def generate_launch_description():
    # All vendor topic names and driver arguments are confined to this provider
    # adapter. Generic Atlas nodes consume only /atlas/spatial/*.
    device_id = LaunchConfiguration("device_id")
    driver = IncludeLaunchDescription(
        PythonLaunchDescriptionSource(
            PathJoinSubstitution([FindPackageShare("depthai_ros_driver_v3"), "launch", "driver.launch.py"])
        ),
        launch_arguments={
            "rs_compat": "true",
            "driver.i_device_id": device_id,
            "rgb.i_synced": "true",
            "stereo.i_synced": "true",
            "stereo.i_aligned": "true",
        }.items(),
    )
    remaps = [
        SetRemap(src="/camera/camera/color/image_raw", dst="/atlas/spatial/color/image_raw"),
        SetRemap(src="/camera/camera/color/camera_info", dst="/atlas/spatial/color/camera_info"),
        SetRemap(src="/camera/camera/depth/image_rect_raw", dst="/atlas/spatial/provider/depth_mm"),
        SetRemap(src="/camera/camera/depth/camera_info", dst="/atlas/spatial/aligned_depth/camera_info"),
    ]
    normalizer = Node(
        package="atlas_spatial_runtime",
        executable="atlas-spatial-depth-normalizer",
        name="atlas_spatial_depth_normalizer",
        output="screen",
    )
    return LaunchDescription([DeclareLaunchArgument("device_id", default_value=""), GroupAction([*remaps, driver, normalizer])])
