from launch import LaunchDescription
from launch_ros.actions import Node


def generate_launch_description():
    return LaunchDescription(
        [
            Node(
                package="atlas_spatial_runtime",
                executable="atlas-spatial-synthetic",
                name="atlas_spatial_synthetic_provider",
                output="screen",
            )
        ]
    )
