fn main() {
    tonic_build::configure()
        .build_client(true)
        .build_server(true)
        .compile_protos(
            &["../../proto/atlas/ground_station.proto"],
            &["../../proto"],
        )
        .expect("compile Atlas ground-station protobuf");
    tauri_build::build()
}
