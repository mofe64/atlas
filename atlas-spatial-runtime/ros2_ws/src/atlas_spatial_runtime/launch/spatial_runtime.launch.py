from launch import LaunchDescription
from launch.actions import DeclareLaunchArgument, IncludeLaunchDescription
from launch.conditions import IfCondition
from launch.launch_description_sources import PythonLaunchDescriptionSource
from launch.substitutions import LaunchConfiguration, PathJoinSubstitution, PythonExpression
from launch_ros.actions import Node
from launch_ros.substitutions import FindPackageShare


def generate_launch_description():
    provider = LaunchConfiguration("provider")
    source_id = LaunchConfiguration("source_id")
    device_id = LaunchConfiguration("device_id")
    socket_path = LaunchConfiguration("socket_path")
    model = LaunchConfiguration("model")
    usb_transport = LaunchConfiguration("usb_transport")
    package_share = FindPackageShare("atlas_spatial_runtime")
    synthetic_launch = PathJoinSubstitution([package_share, "launch", "providers", "synthetic.launch.py"])
    depthai_launch = PathJoinSubstitution([package_share, "launch", "providers", "depthai.launch.py"])
    return LaunchDescription(
        [
            DeclareLaunchArgument("provider", default_value="synthetic", choices=["synthetic", "depthai"]),
            DeclareLaunchArgument("source_id", default_value="front-depth"),
            DeclareLaunchArgument("device_id", default_value=""),
            DeclareLaunchArgument("model", default_value=""),
            DeclareLaunchArgument("usb_transport", default_value="unknown"),
            DeclareLaunchArgument("socket_path", default_value="/run/atlas-agent/spatial.sock"),
            IncludeLaunchDescription(
                PythonLaunchDescriptionSource(synthetic_launch),
                condition=IfCondition(PythonExpression(["'", provider, "' == 'synthetic'"])),
            ),
            IncludeLaunchDescription(
                PythonLaunchDescriptionSource(depthai_launch),
                condition=IfCondition(PythonExpression(["'", provider, "' == 'depthai'"])),
                launch_arguments={"device_id": device_id}.items(),
            ),
            Node(
                package="atlas_spatial_runtime",
                executable="atlas-spatial-health",
                name="atlas_spatial_health",
                output="screen",
                parameters=[
                    {
                        "provider": provider,
                        "source_id": source_id,
                        "device_id": device_id,
                        "model": model,
                        "usb_transport": usb_transport,
                        "socket_path": socket_path,
                    }
                ],
            ),
        ]
    )
