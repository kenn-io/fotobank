import { mount } from "svelte";
import App from "./App.svelte";
import "@kenn-io/kit-ui/theme.css";
import "./app.css";

const target = document.getElementById("app");
if (!target) throw new Error("missing #app root");
mount(App, { target });
