from launch import LaunchDescription
from launch.actions import DeclareLaunchArgument, IncludeLaunchDescription, Shutdown
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
    cloud_socket_path = LaunchConfiguration("cloud_socket_path")
    model = LaunchConfiguration("model")
    usb_transport = LaunchConfiguration("usb_transport")
    transform_bundle_path = LaunchConfiguration("transform_bundle_path")
    vio_enabled = LaunchConfiguration("vio_enabled")
    live_cloud_enabled = LaunchConfiguration("live_cloud_enabled")
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
            DeclareLaunchArgument(
                "cloud_socket_path", default_value="/run/atlas-agent/spatial-cloud.sock"
            ),
            DeclareLaunchArgument(
                "transform_bundle_path",
                default_value=PathJoinSubstitution([package_share, "config", "transforms.v1.json"]),
            ),
            # This flag controls estimator execution, not authority: Basalt
            # publishes every live VIO sample, but cannot command PX4.
            DeclareLaunchArgument("vio_enabled", default_value="true", choices=["true", "false"]),
            DeclareLaunchArgument("live_cloud_enabled", default_value="true", choices=["true", "false"]),
            IncludeLaunchDescription(
                PythonLaunchDescriptionSource(synthetic_launch),
                condition=IfCondition(PythonExpression(["'", provider, "' == 'synthetic'"])),
            ),
            IncludeLaunchDescription(
                PythonLaunchDescriptionSource(depthai_launch),
                condition=IfCondition(PythonExpression(["'", provider, "' == 'depthai'"])),
                launch_arguments={"device_id": device_id, "vio_enabled": vio_enabled}.items(),
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
                        "imu_required": True,
                        "transform_bundle_path": transform_bundle_path,
                        "provider_startup_grace_ms": 30000.0,
                        "provider_stale_exit_after_ms": 5000.0,
                    }
                ],
                on_exit=Shutdown(reason="spatial provider health monitor exited"),
            ),
            Node(
                package="atlas_spatial_runtime",
                executable="atlas-spatial-live-cloud",
                name="atlas_live_cloud",
                output="screen",
                condition=IfCondition(
                    PythonExpression([
                        "'", live_cloud_enabled, "' == 'true' and '", vio_enabled, "' == 'true'"
                    ])
                ),
                parameters=[{"transform_bundle_path": transform_bundle_path}],
            ),
            Node(
                package="atlas_spatial_runtime",
                executable="atlas-spatial-stream",
                name="atlas_spatial_stream",
                output="screen",
                condition=IfCondition(
                    PythonExpression([
                        "'", live_cloud_enabled, "' == 'true' and '", vio_enabled, "' == 'true'"
                    ])
                ),
                parameters=[
                    {
                        "source_id": source_id,
                        "cloud_socket_path": cloud_socket_path,
                        "maximum_points": 100000,
                        "voxel_size_m": 0.05,
                    }
                ],
            ),
        ]
    )
