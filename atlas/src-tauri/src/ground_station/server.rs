use std::{net::SocketAddr, sync::Arc};

use tonic::{transport::Server, Request, Response, Status, Streaming};

use crate::database::LocalDatabase;

use super::{
    command_router::CommandRouter,
    perception::{self, PerceptionResponseStream, PerceptionStore},
    proto::pb::{
        ground_station_service_server::{
            GroundStationService as GroundStationServiceContract, GroundStationServiceServer,
        },
        AgentToGroundStation,
    },
    session::{self, SessionResponseStream},
};

#[derive(Clone)]
struct GroundStationService {
    database: Arc<LocalDatabase>,
    command_router: CommandRouter,
    perception: PerceptionStore,
}

#[tonic::async_trait]
impl GroundStationServiceContract for GroundStationService {
    type OpenSessionStream = SessionResponseStream;
    type OpenPerceptionStreamStream = PerceptionResponseStream;

    async fn open_session(
        &self,
        request: Request<Streaming<AgentToGroundStation>>,
    ) -> Result<Response<Self::OpenSessionStream>, Status> {
        session::open(
            Arc::clone(&self.database),
            self.command_router.clone(),
            request,
        )
        .await
    }

    async fn open_perception_stream(
        &self,
        request: Request<Streaming<super::proto::pb::AgentPerception>>,
    ) -> Result<Response<Self::OpenPerceptionStreamStream>, Status> {
        perception::open(Arc::clone(&self.database), self.perception.clone(), request).await
    }
}

pub(crate) async fn serve(
    address: SocketAddr,
    database: Arc<LocalDatabase>,
    command_router: CommandRouter,
    perception: PerceptionStore,
) -> Result<(), String> {
    println!("Atlas ground station listening for agents on {address}");
    Server::builder()
        .add_service(GroundStationServiceServer::new(GroundStationService {
            database,
            command_router,
            perception,
        }))
        .serve(address)
        .await
        .map_err(|error| format!("run Atlas ground-station gRPC server: {error}"))
}
