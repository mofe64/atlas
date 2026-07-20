from glob import glob
from setuptools import find_packages, setup

package_name = "atlas_spatial_runtime"

setup(
    name=package_name,
    version="0.1.0",
    packages=find_packages(exclude=("test",)),
    data_files=[
        ("share/ament_index/resource_index/packages", ["resource/" + package_name]),
        ("share/" + package_name, ["package.xml"]),
        ("share/" + package_name + "/launch", glob("launch/*.launch.py")),
        ("share/" + package_name + "/launch/providers", glob("launch/providers/*.launch.py")),
    ],
    install_requires=["setuptools"],
    zip_safe=True,
    maintainer="Sunnyside Engineering",
    maintainer_email="engineering@sunnyside.local",
    description="Vendor-neutral synchronized RGB-D boundary and health service for Atlas.",
    license="Proprietary",
    entry_points={
        "console_scripts": [
            "atlas-spatial-health = atlas_spatial_runtime.health_node:main",
            "atlas-spatial-depth-normalizer = atlas_spatial_runtime.depth_normalizer:main",
            "atlas-spatial-probe = atlas_spatial_runtime.probe:main",
            "atlas-spatial-synthetic = atlas_spatial_runtime.synthetic_provider:main",
        ],
    },
)
