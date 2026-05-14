import { defineConfig } from "astro/config";
import tailwind from "@astrojs/tailwind";

export default defineConfig({
  site: "https://tribunal.mabus.ai",
  integrations: [tailwind()],
  server: {
    host: "0.0.0.0",
    port: 4321,
  },
});
