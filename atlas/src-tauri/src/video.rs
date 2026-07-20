use std::{
    collections::VecDeque,
    env,
    io::{BufRead, BufReader, Read},
    process::{Child, Command, Stdio},
    sync::{Arc, Mutex},
    thread,
};

use serde::Serialize;

use crate::{database::unix_time_ms, ground_station::PerceptionStore};

const FRAME_PACKET_MAGIC: &[u8; 4] = b"ATV1";
const MAX_JPEG_BYTES: usize = 8 * 1024 * 1024;
const MAX_BUFFERED_FRAMES: usize = 120;

#[derive(Clone)]
pub(crate) struct VideoManager {
    config: VideoConfig,
    shared: Arc<VideoShared>,
}

struct VideoShared {
    state: Mutex<VideoState>,
}

#[derive(Clone)]
struct VideoConfig {
    rtsp_url: String,
    rtsp_transport: String,
    decoder_path: String,
    source_id: String,
    width: u32,
    height: u32,
    frames_per_second: u32,
    jpeg_quality: u32,
    playout_delay_ms: i64,
    alignment_tolerance_ms: i64,
    overlay_offset_ms: i64,
}

struct VideoState {
    generation: u64,
    status: String,
    drone_id: Option<String>,
    started_at_unix_ms: Option<i64>,
    last_frame_at_unix_ms: Option<i64>,
    last_error: Option<String>,
    last_decoder_message: Option<String>,
    sequence: u64,
    dropped_frames: u64,
    frames: VecDeque<DecodedFrame>,
    child: Option<Arc<Mutex<Option<Child>>>>,
}

#[derive(Clone)]
struct DecodedFrame {
    sequence: u64,
    received_at_unix_ms: i64,
    jpeg: Arc<Vec<u8>>,
}

#[derive(Debug, Clone)]
pub(crate) struct CapturedVideoFrame {
    pub(crate) source_id: String,
    pub(crate) observed_at_unix_ms: i64,
    pub(crate) jpeg: Vec<u8>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct VideoStreamSnapshot {
    pub(crate) status: String,
    pub(crate) drone_id: Option<String>,
    source_id: String,
    width: u32,
    height: u32,
    target_frames_per_second: u32,
    playout_delay_ms: i64,
    alignment_tolerance_ms: i64,
    overlay_offset_ms: i64,
    pub(crate) started_at_unix_ms: Option<i64>,
    pub(crate) last_frame_at_unix_ms: Option<i64>,
    latest_sequence: u64,
    dropped_frames: u64,
    pub(crate) last_error: Option<String>,
}

#[derive(Clone)]
pub(crate) struct VideoSourceConfig {
    pub(crate) rtsp_url: String,
    pub(crate) rtsp_transport: String,
    pub(crate) decoder_path: String,
    pub(crate) source_id: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct VideoFrameHeader {
    sequence: u64,
    received_at_unix_ms: i64,
    width: u32,
    height: u32,
    mime_type: &'static str,
    overlay: Option<crate::ground_station::AlignedPerceptionFrameSnapshot>,
}

impl VideoManager {
    pub(crate) fn from_environment() -> Result<Self, String> {
        let config = VideoConfig::from_environment()?;
        Ok(Self::with_config(config))
    }

    fn with_config(config: VideoConfig) -> Self {
        Self {
            config,
            shared: Arc::new(VideoShared {
                state: Mutex::new(VideoState {
                    generation: 0,
                    status: "stopped".to_string(),
                    drone_id: None,
                    started_at_unix_ms: None,
                    last_frame_at_unix_ms: None,
                    last_error: None,
                    last_decoder_message: None,
                    sequence: 0,
                    dropped_frames: 0,
                    frames: VecDeque::new(),
                    child: None,
                }),
            }),
        }
    }

    pub(crate) fn start(&self, drone_id: &str) -> Result<VideoStreamSnapshot, String> {
        let drone_id = drone_id.trim();
        if drone_id.is_empty() {
            return Err("video stream requires a drone id".to_string());
        }
        let mut state = self.lock_state()?;
        if matches!(state.status.as_str(), "connecting" | "playing")
            && state.drone_id.as_deref() == Some(drone_id)
        {
            return Ok(self.snapshot_from(&state));
        }

        stop_process(&mut state);
        state.generation = state.generation.wrapping_add(1);
        let generation = state.generation;
        state.status = "connecting".to_string();
        state.drone_id = Some(drone_id.to_string());
        state.started_at_unix_ms = Some(unix_time_ms());
        state.last_frame_at_unix_ms = None;
        state.last_error = None;
        state.last_decoder_message = None;
        state.sequence = 0;
        state.dropped_frames = 0;
        state.frames.clear();

        let mut child = match self.decoder_command().spawn() {
            Ok(child) => child,
            Err(error) => {
                state.status = "error".to_string();
                let message = format!(
                    "start native video decoder {}: {error}",
                    self.config.decoder_path
                );
                state.last_error = Some(message.clone());
                return Err(message);
            }
        };
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| "native video decoder did not expose stdout".to_string())?;
        let stderr = child
            .stderr
            .take()
            .ok_or_else(|| "native video decoder did not expose stderr".to_string())?;
        let child = Arc::new(Mutex::new(Some(child)));
        state.child = Some(Arc::clone(&child));
        drop(state);

        let stderr_shared = Arc::clone(&self.shared);
        thread::Builder::new()
            .name("atlas-video-decoder-errors".to_string())
            .spawn(move || read_decoder_errors(stderr_shared, generation, stderr))
            .map_err(|error| format!("start video decoder error reader: {error}"))?;

        let frame_shared = Arc::clone(&self.shared);
        thread::Builder::new()
            .name("atlas-video-decoder-frames".to_string())
            .spawn(move || read_decoder_frames(frame_shared, generation, child, stdout))
            .map_err(|error| format!("start video decoder frame reader: {error}"))?;

        self.snapshot()
    }

    pub(crate) fn stop(&self, drone_id: Option<&str>) -> Result<VideoStreamSnapshot, String> {
        let mut state = self.lock_state()?;
        if drone_id.is_some_and(|candidate| state.drone_id.as_deref() != Some(candidate)) {
            return Ok(self.snapshot_from(&state));
        }
        state.generation = state.generation.wrapping_add(1);
        stop_process(&mut state);
        state.status = "stopped".to_string();
        state.drone_id = None;
        state.started_at_unix_ms = None;
        state.last_frame_at_unix_ms = None;
        state.last_error = None;
        state.last_decoder_message = None;
        state.frames.clear();
        Ok(self.snapshot_from(&state))
    }

    pub(crate) fn snapshot(&self) -> Result<VideoStreamSnapshot, String> {
        let state = self.lock_state()?;
        Ok(self.snapshot_from(&state))
    }

    pub(crate) fn source_config(&self) -> VideoSourceConfig {
        VideoSourceConfig {
            rtsp_url: self.config.rtsp_url.clone(),
            rtsp_transport: self.config.rtsp_transport.clone(),
            decoder_path: self.config.decoder_path.clone(),
            source_id: self.config.source_id.clone(),
        }
    }

    pub(crate) fn latest_frame(&self, drone_id: &str) -> Result<CapturedVideoFrame, String> {
        let state = self.lock_state()?;
        if state.drone_id.as_deref() != Some(drone_id) || state.status != "playing" {
            return Err("still capture requires the aircraft's live video stream".into());
        }
        let frame = state
            .frames
            .back()
            .ok_or_else(|| "still capture requires a decoded video frame".to_string())?;
        Ok(CapturedVideoFrame {
            source_id: self.config.source_id.clone(),
            observed_at_unix_ms: frame.received_at_unix_ms,
            jpeg: frame.jpeg.as_ref().clone(),
        })
    }

    pub(crate) fn frame_packet(
        &self,
        perception: &PerceptionStore,
        drone_id: &str,
        after_sequence: u64,
    ) -> Result<Vec<u8>, String> {
        let frame = {
            let state = self.lock_state()?;
            if state.drone_id.as_deref() != Some(drone_id) || state.status != "playing" {
                return Ok(Vec::new());
            }
            let playout_cutoff = unix_time_ms() - self.config.playout_delay_ms;
            state
                .frames
                .iter()
                .rev()
                .find(|frame| {
                    frame.sequence > after_sequence && frame.received_at_unix_ms <= playout_cutoff
                })
                .cloned()
        };
        let Some(frame) = frame else {
            return Ok(Vec::new());
        };
        let header = VideoFrameHeader {
            sequence: frame.sequence,
            received_at_unix_ms: frame.received_at_unix_ms,
            width: self.config.width,
            height: self.config.height,
            mime_type: "image/jpeg",
            overlay: perception.aligned_frame(
                drone_id,
                &self.config.source_id,
                frame.received_at_unix_ms,
                self.config.overlay_offset_ms,
                self.config.alignment_tolerance_ms,
            ),
        };
        encode_frame_packet(&header, &frame.jpeg)
    }

    fn lock_state(&self) -> Result<std::sync::MutexGuard<'_, VideoState>, String> {
        self.shared
            .state
            .lock()
            .map_err(|_| "native video state lock was poisoned".to_string())
    }

    fn snapshot_from(&self, state: &VideoState) -> VideoStreamSnapshot {
        VideoStreamSnapshot {
            status: state.status.clone(),
            drone_id: state.drone_id.clone(),
            source_id: self.config.source_id.clone(),
            width: self.config.width,
            height: self.config.height,
            target_frames_per_second: self.config.frames_per_second,
            playout_delay_ms: self.config.playout_delay_ms,
            alignment_tolerance_ms: self.config.alignment_tolerance_ms,
            overlay_offset_ms: self.config.overlay_offset_ms,
            started_at_unix_ms: state.started_at_unix_ms,
            last_frame_at_unix_ms: state.last_frame_at_unix_ms,
            latest_sequence: state.sequence,
            dropped_frames: state.dropped_frames,
            last_error: state.last_error.clone(),
        }
    }

    fn decoder_command(&self) -> Command {
        let scale = format!(
            "fps={},scale={}:{}:force_original_aspect_ratio=decrease,pad={}:{}:(ow-iw)/2:(oh-ih)/2",
            self.config.frames_per_second,
            self.config.width,
            self.config.height,
            self.config.width,
            self.config.height,
        );
        let mut command = Command::new(&self.config.decoder_path);
        command
            .args([
                "-nostdin",
                "-hide_banner",
                "-loglevel",
                "warning",
                "-rtsp_transport",
                &self.config.rtsp_transport,
                "-fflags",
                "nobuffer",
                "-flags",
                "low_delay",
                "-analyzeduration",
                "1000000",
                "-probesize",
                "1000000",
                "-i",
                &self.config.rtsp_url,
                "-map",
                "0:v:0",
                "-an",
                "-sn",
                "-dn",
                "-vf",
                &scale,
                "-c:v",
                "mjpeg",
                "-q:v",
                &self.config.jpeg_quality.to_string(),
                "-f",
                "image2pipe",
                "pipe:1",
            ])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        command
    }
}

impl VideoConfig {
    fn from_environment() -> Result<Self, String> {
        let rtsp_url = environment_or(
            "ATLAS_VIDEO_RTSP_URL",
            "rtsp://192.168.144.25:8554/main.264",
        );
        if !(rtsp_url.starts_with("rtsp://") || rtsp_url.starts_with("rtsps://"))
            || rtsp_url.chars().any(char::is_whitespace)
        {
            return Err(
                "ATLAS_VIDEO_RTSP_URL must be an rtsp:// or rtsps:// URL without whitespace"
                    .to_string(),
            );
        }
        let decoder_path = environment_or("ATLAS_VIDEO_DECODER_PATH", "ffmpeg");
        if decoder_path.trim().is_empty() {
            return Err("ATLAS_VIDEO_DECODER_PATH cannot be empty".to_string());
        }
        let source_id = environment_or("ATLAS_VIDEO_SOURCE_ID", "a8-main");
        if source_id.trim().is_empty() || source_id.len() > 128 {
            return Err("ATLAS_VIDEO_SOURCE_ID must contain 1 to 128 characters".to_string());
        }
        let rtsp_transport = environment_or("ATLAS_VIDEO_RTSP_TRANSPORT", "tcp").to_lowercase();
        if !matches!(rtsp_transport.as_str(), "tcp" | "udp") {
            return Err("ATLAS_VIDEO_RTSP_TRANSPORT must be tcp or udp".to_string());
        }
        Ok(Self {
            rtsp_url,
            rtsp_transport,
            decoder_path,
            source_id,
            width: environment_number("ATLAS_VIDEO_WIDTH", 1280, 320, 3840)?,
            height: environment_number("ATLAS_VIDEO_HEIGHT", 720, 180, 2160)?,
            frames_per_second: environment_number("ATLAS_VIDEO_FPS", 15, 1, 30)?,
            jpeg_quality: environment_number("ATLAS_VIDEO_JPEG_QUALITY", 5, 2, 31)?,
            playout_delay_ms: environment_number("ATLAS_VIDEO_PLAYOUT_DELAY_MS", 350, 0, 2_000)?,
            alignment_tolerance_ms: environment_number(
                "ATLAS_VIDEO_ALIGNMENT_TOLERANCE_MS",
                180,
                10,
                1_000,
            )?,
            overlay_offset_ms: environment_number(
                "ATLAS_VIDEO_OVERLAY_OFFSET_MS",
                0,
                -2_000,
                2_000,
            )?,
        })
    }
}

fn environment_or(name: &str, fallback: &str) -> String {
    env::var(name)
        .ok()
        .filter(|value| !value.trim().is_empty())
        .unwrap_or_else(|| fallback.to_string())
}

fn environment_number<T>(name: &str, fallback: T, minimum: T, maximum: T) -> Result<T, String>
where
    T: Copy + Ord + std::str::FromStr + std::fmt::Display,
{
    let value = match env::var(name) {
        Ok(raw) if !raw.trim().is_empty() => raw
            .parse::<T>()
            .map_err(|_| format!("{name} must be a number"))?,
        _ => fallback,
    };
    if value < minimum || value > maximum {
        return Err(format!("{name} must be between {minimum} and {maximum}"));
    }
    Ok(value)
}

fn stop_process(state: &mut VideoState) {
    let Some(child) = state.child.take() else {
        return;
    };
    if let Ok(mut child) = child.lock() {
        if let Some(child) = child.as_mut() {
            let _ = child.kill();
        }
    };
}

fn read_decoder_errors(shared: Arc<VideoShared>, generation: u64, stderr: impl Read) {
    for line in BufReader::new(stderr).lines().map_while(Result::ok) {
        let message = line.trim();
        if message.is_empty() {
            continue;
        }
        if let Ok(mut state) = shared.state.lock() {
            if state.generation != generation {
                return;
            }
            state.last_decoder_message = Some(message.chars().take(1_000).collect());
        }
    }
}

fn read_decoder_frames(
    shared: Arc<VideoShared>,
    generation: u64,
    child: Arc<Mutex<Option<Child>>>,
    mut stdout: impl Read,
) {
    let mut parser = MjpegParser::default();
    let mut chunk = [0_u8; 64 * 1024];
    let mut terminal_error = None;
    'decoder: loop {
        match stdout.read(&mut chunk) {
            Ok(0) => break,
            Ok(read) => match parser.push(&chunk[..read]) {
                Ok(frames) => {
                    for jpeg in frames {
                        if !publish_decoded_frame(&shared, generation, jpeg) {
                            break 'decoder;
                        }
                    }
                }
                Err(error) => {
                    terminal_error = Some(error);
                    break;
                }
            },
            Err(error) => {
                terminal_error = Some(format!("read decoded video frame: {error}"));
                break;
            }
        }
    }

    let exit = child
        .lock()
        .ok()
        .and_then(|mut child| child.take())
        .and_then(|mut child| child.wait().ok());
    if let Ok(mut state) = shared.state.lock() {
        if state.generation != generation {
            return;
        }
        state.child = None;
        let was_playing = state.sequence > 0;
        state.status = "error".to_string();
        state.last_error = terminal_error.or_else(|| {
            state.last_decoder_message.clone().or_else(|| {
                Some(match exit {
                    Some(status) => format!("native video decoder exited with {status}"),
                    None if was_playing => "native video decoder stream ended".to_string(),
                    None => "native video decoder ended before producing a frame".to_string(),
                })
            })
        });
    }
}

fn publish_decoded_frame(shared: &VideoShared, generation: u64, jpeg: Vec<u8>) -> bool {
    let Ok(mut state) = shared.state.lock() else {
        return false;
    };
    if state.generation != generation {
        return false;
    }
    state.sequence = state.sequence.wrapping_add(1);
    let received_at = unix_time_ms();
    let sequence = state.sequence;
    state.frames.push_back(DecodedFrame {
        sequence,
        received_at_unix_ms: received_at,
        jpeg: Arc::new(jpeg),
    });
    while state.frames.len() > MAX_BUFFERED_FRAMES {
        state.frames.pop_front();
        state.dropped_frames = state.dropped_frames.saturating_add(1);
    }
    state.status = "playing".to_string();
    state.last_frame_at_unix_ms = Some(received_at);
    true
}

fn encode_frame_packet(header: &VideoFrameHeader, jpeg: &[u8]) -> Result<Vec<u8>, String> {
    let header = serde_json::to_vec(header)
        .map_err(|error| format!("serialize native video frame header: {error}"))?;
    let header_length = u32::try_from(header.len())
        .map_err(|_| "native video frame header exceeds protocol limits".to_string())?;
    let mut packet = Vec::with_capacity(8 + header.len() + jpeg.len());
    packet.extend_from_slice(FRAME_PACKET_MAGIC);
    packet.extend_from_slice(&header_length.to_le_bytes());
    packet.extend_from_slice(&header);
    packet.extend_from_slice(jpeg);
    Ok(packet)
}

#[derive(Default)]
struct MjpegParser {
    buffer: Vec<u8>,
}

impl MjpegParser {
    fn push(&mut self, bytes: &[u8]) -> Result<Vec<Vec<u8>>, String> {
        self.buffer.extend_from_slice(bytes);
        let mut frames = Vec::new();
        loop {
            let Some(start) = marker_position(&self.buffer, 0xff, 0xd8, 0) else {
                if self.buffer.last() == Some(&0xff) {
                    self.buffer.drain(..self.buffer.len().saturating_sub(1));
                } else {
                    self.buffer.clear();
                }
                break;
            };
            if start > 0 {
                self.buffer.drain(..start);
            }
            let Some(end) = marker_position(&self.buffer, 0xff, 0xd9, 2) else {
                if self.buffer.len() > MAX_JPEG_BYTES {
                    return Err("native decoder produced an oversized JPEG frame".to_string());
                }
                break;
            };
            frames.push(self.buffer.drain(..end + 2).collect());
        }
        Ok(frames)
    }
}

fn marker_position(bytes: &[u8], first: u8, second: u8, from: usize) -> Option<usize> {
    bytes
        .get(from..)?
        .windows(2)
        .position(|window| window == [first, second])
        .map(|position| position + from)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mjpeg_parser_handles_fragmented_and_consecutive_frames() {
        let mut parser = MjpegParser::default();
        assert!(parser.push(&[9, 0xff]).expect("first fragment").is_empty());
        let frames = parser
            .push(&[0xd8, 1, 2, 0xff, 0xd9, 0xff, 0xd8, 3, 0xff])
            .expect("middle fragment");
        assert_eq!(frames, vec![vec![0xff, 0xd8, 1, 2, 0xff, 0xd9]]);
        let frames = parser.push(&[0xd9]).expect("final fragment");
        assert_eq!(frames, vec![vec![0xff, 0xd8, 3, 0xff, 0xd9]]);
    }

    #[test]
    fn frame_packet_keeps_header_and_clean_jpeg_separate() {
        let header = VideoFrameHeader {
            sequence: 7,
            received_at_unix_ms: 42,
            width: 1280,
            height: 720,
            mime_type: "image/jpeg",
            overlay: None,
        };
        let jpeg = [0xff, 0xd8, 1, 2, 0xff, 0xd9];
        let packet = encode_frame_packet(&header, &jpeg).expect("encode packet");
        assert_eq!(&packet[..4], FRAME_PACKET_MAGIC);
        let header_length =
            u32::from_le_bytes(packet[4..8].try_into().expect("header length")) as usize;
        let value: serde_json::Value =
            serde_json::from_slice(&packet[8..8 + header_length]).expect("decode header");
        assert_eq!(value["sequence"], 7);
        assert_eq!(&packet[8 + header_length..], jpeg);
    }

    #[cfg(unix)]
    #[test]
    fn supervised_decoder_publishes_a_pull_based_frame() {
        use std::{fs, os::unix::fs::PermissionsExt, time::Duration};

        let decoder = std::env::temp_dir().join(format!(
            "atlas-video-decoder-{}-{}.sh",
            std::process::id(),
            unix_time_ms()
        ));
        fs::write(
            &decoder,
            "#!/bin/sh\nprintf '\\377\\330\\001\\377\\331'\nsleep 1\n",
        )
        .expect("write fake decoder");
        fs::set_permissions(&decoder, fs::Permissions::from_mode(0o700))
            .expect("protect fake decoder");
        let manager = VideoManager::with_config(VideoConfig {
            rtsp_url: "rtsp://camera/main".into(),
            rtsp_transport: "tcp".into(),
            decoder_path: decoder.to_string_lossy().into_owned(),
            source_id: "a8-main".into(),
            width: 640,
            height: 360,
            frames_per_second: 15,
            jpeg_quality: 5,
            playout_delay_ms: 0,
            alignment_tolerance_ms: 180,
            overlay_offset_ms: 0,
        });
        manager.start("drone-1").expect("start fake decoder");
        for _ in 0..50 {
            if manager.snapshot().expect("video snapshot").status == "playing" {
                break;
            }
            thread::sleep(Duration::from_millis(10));
        }
        let packet = manager
            .frame_packet(&PerceptionStore::default(), "drone-1", 0)
            .expect("read native frame packet");
        assert!(!packet.is_empty());
        assert_eq!(&packet[..4], FRAME_PACKET_MAGIC);
        manager.stop(Some("drone-1")).expect("stop fake decoder");
        let _ = fs::remove_file(decoder);
    }
}
