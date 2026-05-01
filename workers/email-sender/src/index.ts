import { Agent, routeAgentRequest } from "agents";
import emailData from "./emailData.json";

const FROM_EMAIL = "o33455677899@gmail.com";
const FROM_NAME = "Email Tester";

interface Env {
  EmailSenderAgent: DurableObjectNamespace;
  GMAIL_CLIENT_ID: string;
  GMAIL_CLIENT_SECRET: string;
  GMAIL_REFRESH_TOKEN: string;
  RECIPIENTS: string;
}

interface EmailEntry {
  text: string;
  type: string;
}

interface AgentState {
  running: boolean;
}

export class EmailSenderAgent extends Agent<Env, AgentState> {
  initialState: AgentState = { running: false };

  private scheduleNext() {
    const delaySecs = Math.floor(Math.random() * 30) + 1;
    this.schedule(delaySecs, "sendNextEmail");
  }

  async sendNextEmail() {
    if (!this.state.running) return;

    const recipients = this.env.RECIPIENTS.split(",").map((r) => r.trim()).filter(Boolean);
    const entry = this.pickRandom(emailData as EmailEntry[]);
    const recipient = this.pickRandom(recipients);

    try {
      const accessToken = await this.getAccessToken();
      await this.sendViaGmail(accessToken, recipient, entry);
      console.log(`Sent "${entry.type}" to ${recipient}`);
    } catch (err) {
      console.error("Failed to send email:", err);
    }

    this.scheduleNext();
  }

  async onRequest(request: Request): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname.endsWith("/start") && request.method === "POST") {
      this.setState({ running: true });
      this.scheduleNext();
      return Response.json({ status: "started" });
    }

    if (url.pathname.endsWith("/stop") && request.method === "POST") {
      this.setState({ running: false });
      return Response.json({ status: "stopped" });
    }

    return Response.json({
      running: this.state.running,
      routes: {
        "POST /start": "begin sending emails",
        "POST /stop": "stop after current in-flight email",
      },
    });
  }

  private pickRandom<T>(arr: T[]): T {
    return arr[Math.floor(Math.random() * arr.length)];
  }

  private async getAccessToken(): Promise<string> {
    const res = await fetch("https://oauth2.googleapis.com/token", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        client_id: this.env.GMAIL_CLIENT_ID,
        client_secret: this.env.GMAIL_CLIENT_SECRET,
        refresh_token: this.env.GMAIL_REFRESH_TOKEN,
        grant_type: "refresh_token",
      }),
    });

    const data = await res.json<{ access_token: string }>();
    return data.access_token;
  }

  private async sendViaGmail(accessToken: string, to: string, entry: EmailEntry): Promise<void> {
    const subject = entry.type === "Phishing Email" ? "Important: Action Required" : "Hello from Email Tester";
    const raw = this.buildRawEmail(to, subject, entry.text);

    const res = await fetch("https://gmail.googleapis.com/gmail/v1/users/me/messages/send", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessToken}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ raw }),
    });

    if (!res.ok) {
      const err = await res.text();
      throw new Error(`Gmail API error: ${err}`);
    }
  }

  private buildRawEmail(to: string, subject: string, body: string): string {
    const message = [
      `From: ${FROM_NAME} <${FROM_EMAIL}>`,
      `To: ${to}`,
      `Subject: ${subject}`,
      `MIME-Version: 1.0`,
      `Content-Type: text/plain; charset=utf-8`,
      ``,
      body,
    ].join("\r\n");

    return btoa(unescape(encodeURIComponent(message)))
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
  }
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext) {
    return (
      (await routeAgentRequest(request, env)) ??
      new Response("Not found", { status: 404 })
    );
  },
} satisfies ExportedHandler<Env>;
