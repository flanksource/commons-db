export type ConnectionPresence = {
  configured: boolean;
  resolved: boolean;
};

export type ConnectionInfo = {
  connection: {
    name: string;
    type: string;
    namespace?: string;
    configuredEndpoint?: string;
    resolvedEndpoint?: string;
    configuredUsername?: string;
    resolvedUsername?: string;
    password: ConnectionPresence;
    certificate: ConnectionPresence;
  };
  server: {
    status: "available" | "unavailable" | "error";
    product?: string;
    version?: string;
    database?: string;
    user?: string;
    cluster?: string;
    node?: string;
    details?: Record<string, string>;
    message?: string;
  };
  discoveredAt: string;
};
