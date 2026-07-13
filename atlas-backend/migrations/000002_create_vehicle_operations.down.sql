DROP TABLE IF EXISTS vehicle_agent_enrollment_tokens;
DROP TABLE IF EXISTS communication_links;
DROP TABLE IF EXISTS vehicle_agent_bindings;
DROP TABLE IF EXISTS vehicle_agents;
DROP TABLE IF EXISTS drones;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_organization_id_id_key;
