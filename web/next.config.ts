import type { NextConfig } from "next";

const config: NextConfig = {
  basePath: "/_",
  output: "export",
  poweredByHeader: false,
  trailingSlash: true,
};

export default config;
