use std::{net::SocketAddr, sync::Arc};

use tonic::{transport::Server, Request, Response, Status, Streaming};

use crate::database::LocalDatabase;

use super::{
    command_router::CommandRouter,
    indoor::IndoorExploreStore,
    perception::{self, PerceptionResponseStream, PerceptionStore},
    proto::pb::{
        ground_station_service_server::{
            GroundStationService as GroundStationServiceContract, GroundStationServiceServer,
        },
        AgentToGroundStation,
    },
    session::{self, SessionResponseStream},
    spatial::{self, SpatialResponseStream, SpatialStore},
};

#[derive(Clone)]
struct GroundStationService {
    database: Arc<LocalDatabase>,
    command_router: CommandRouter,
    indoor: IndoorExploreStore,
    perception: PerceptionStore,
    spatial: SpatialStore,
}

#[tonic::async_trait]
impl GroundStationServiceContract for GroundStationService {
    type OpenSessionStream = SessionResponseStream;
    type OpenPerceptionStreamStream = PerceptionResponseStream;
    type OpenSpatialStreamStream = SpatialResponseStream;

    async fn open_session(
        &self,
        request: Request<Streaming<AgentToGroundStation>>,
    ) -> Result<Response<Self::OpenSessionStream>, Status> {
        session::open(
            Arc::clone(&self.database),
            self.command_router.clone(),
            self.indoor.clone(),
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

    async fn open_spatial_stream(
        &self,
        request: Request<Streaming<super::proto::pb::AgentSpatial>>,
    ) -> Result<Response<Self::OpenSpatialStreamStream>, Status> {
        spatial::open(Arc::clone(&self.database), self.spatial.clone(), request).await
    }
}

pub(crate) async fn serve(
    address: SocketAddr,
    database: Arc<LocalDatabase>,
    command_router: CommandRouter,
    indoor: IndoorExploreStore,
    perception: PerceptionStore,
    spatial: SpatialStore,
) -> Result<(), String> {
    println!("Atlas ground station listening for agents on {address}");
    Server::builder()
        .add_service(GroundStationServiceServer::new(GroundStationService {
            database,
            command_router,
            indoor,
            perception,
            spatial,
        }))
        .serve(address)
        .await
        .map_err(|error| format!("run Atlas ground-station gRPC server: {error}"))
}
