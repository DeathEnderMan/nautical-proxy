package com.nauticalproxy.plugin;

import org.bukkit.plugin.java.JavaPlugin;
import org.bukkit.scheduler.BukkitTask;
import org.bukkit.event.Listener;
import org.bukkit.event.EventHandler;
import org.bukkit.event.player.PlayerJoinEvent;
import org.bukkit.event.player.PlayerQuitEvent;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.command.CommandExecutor;
import org.bukkit.entity.Player;

import com.google.gson.Gson;
import com.google.gson.GsonBuilder;

import java.io.*;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.HashMap;
import java.util.Map;
import java.util.logging.Level;

public class NauticalProxyPlugin extends JavaPlugin implements Listener, CommandExecutor {

    private static final String PROXY_API_URL = "http://localhost:8080/api/v1";
    private String serverId;
    private String serverName;
    private String proxyHost;
    private int proxyPort;
    private BukkitTask heartbeatTask;
    private Gson gson;

    @Override
    public void onEnable() {
        // Initialize Gson
        gson = new GsonBuilder().setPrettyPrinting().create();
        
        // Save default config
        saveDefaultConfig();
        
        // Load configuration
        loadConfiguration();
        
        // Register events
        getServer().getPluginManager().registerEvents(this, this);
        
        // Register commands
        getCommand("nautical").setExecutor(this);
        
        // Start heartbeat task
        startHeartbeat();
        
        // Register with proxy
        registerWithProxy();
        
        getLogger().info("Nautical Proxy Plugin enabled!");
    }

    @Override
    public void onDisable() {
        // Cancel heartbeat task
        if (heartbeatTask != null) {
            heartbeatTask.cancel();
        }
        
        // Unregister from proxy
        unregisterFromProxy();
        
        getLogger().info("Nautical Proxy Plugin disabled!");
    }

    private void loadConfiguration() {
        serverId = getConfig().getString("server.id", "server-" + System.currentTimeMillis());
        serverName = getConfig().getString("server.name", "Minecraft Server");
        proxyHost = getConfig().getString("proxy.host", "localhost");
        proxyPort = getConfig().getInt("proxy.port", 8080);
        
        // Save the generated server ID back to config
        getConfig().set("server.id", serverId);
        saveConfig();
    }

    private void startHeartbeat() {
        heartbeatTask = getServer().getScheduler().runTaskTimerAsynchronously(this, () -> {
            sendHeartbeat();
        }, 0L, 20L * 30); // Every 30 seconds
    }

    private void registerWithProxy() {
        getServer().getScheduler().runTaskAsynchronously(this, () -> {
            try {
                ServerInfo serverInfo = createServerInfo();
                String jsonData = gson.toJson(serverInfo);
                
                // First try to update existing server
                if (!updateServer(jsonData)) {
                    // If update fails, try to add new server
                    addServer(jsonData);
                }
                
                getLogger().info("Successfully registered with proxy as server: " + serverId);
            } catch (Exception e) {
                getLogger().log(Level.WARNING, "Failed to register with proxy: " + e.getMessage());
            }
        });
    }

    private void unregisterFromProxy() {
        try {
            URL url = new URL(PROXY_API_URL + "/servers/" + serverId);
            HttpURLConnection conn = (HttpURLConnection) url.openConnection();
            conn.setRequestMethod("DELETE");
            conn.setRequestProperty("Content-Type", "application/json");
            
            int responseCode = conn.getResponseCode();
            if (responseCode == 200) {
                getLogger().info("Successfully unregistered from proxy");
            }
        } catch (Exception e) {
            getLogger().log(Level.WARNING, "Failed to unregister from proxy: " + e.getMessage());
        }
    }

    private boolean updateServer(String jsonData) {
        try {
            URL url = new URL(PROXY_API_URL + "/servers/" + serverId);
            HttpURLConnection conn = (HttpURLConnection) url.openConnection();
            conn.setRequestMethod("PUT");
            conn.setRequestProperty("Content-Type", "application/json");
            conn.setDoOutput(true);
            
            try (OutputStream os = conn.getOutputStream()) {
                byte[] input = jsonData.getBytes("utf-8");
                os.write(input, 0, input.length);
            }
            
            return conn.getResponseCode() == 200;
        } catch (Exception e) {
            return false;
        }
    }

    private boolean addServer(String jsonData) {
        try {
            URL url = new URL(PROXY_API_URL + "/servers");
            HttpURLConnection conn = (HttpURLConnection) url.openConnection();
            conn.setRequestMethod("POST");
            conn.setRequestProperty("Content-Type", "application/json");
            conn.setDoOutput(true);
            
            try (OutputStream os = conn.getOutputStream()) {
                byte[] input = jsonData.getBytes("utf-8");
                os.write(input, 0, input.length);
            }
            
            return conn.getResponseCode() == 201;
        } catch (Exception e) {
            getLogger().log(Level.WARNING, "Failed to add server to proxy: " + e.getMessage());
            return false;
        }
    }

    private void sendHeartbeat() {
        try {
            ServerInfo serverInfo = createServerInfo();
            String jsonData = gson.toJson(serverInfo);
            
            updateServer(jsonData);
        } catch (Exception e) {
            getLogger().log(Level.WARNING, "Failed to send heartbeat: " + e.getMessage());
        }
    }

    private ServerInfo createServerInfo() {
        ServerInfo info = new ServerInfo();
        info.id = serverId;
        info.name = serverName;
        info.address = getConfig().getString("server.address", "localhost");
        info.port = getServer().getPort();
        info.status = "online";
        info.player_count = getServer().getOnlinePlayers().size();
        info.max_players = getServer().getMaxPlayers();
        info.motd = getServer().getMotd();
        info.version = getServer().getVersion();
        info.last_ping = System.currentTimeMillis();
        info.priority = getConfig().getInt("server.priority", 1);
        
        // Add custom metadata
        info.metadata = new HashMap<>();
        info.metadata.put("world", getServer().getWorlds().get(0).getName());
        info.metadata.put("difficulty", getServer().getWorlds().get(0).getDifficulty().name());
        info.metadata.put("gamemode", getServer().getDefaultGameMode().name());
        info.metadata.put("tps", String.valueOf(getServer().getTPS()[0]));
        info.metadata.put("plugin_version", getDescription().getVersion());
        
        return info;
    }

    @EventHandler
    public void onPlayerJoin(PlayerJoinEvent event) {
        // Send player join notification to proxy
        getServer().getScheduler().runTaskAsynchronously(this, () -> {
            sendPlayerEvent("join", event.getPlayer());
        });
    }

    @EventHandler
    public void onPlayerQuit(PlayerQuitEvent event) {
        // Send player quit notification to proxy
        getServer().getScheduler().runTaskAsynchronously(this, () -> {
            sendPlayerEvent("quit", event.getPlayer());
        });
    }

    private void sendPlayerEvent(String eventType, Player player) {
        try {
            Map<String, Object> eventData = new HashMap<>();
            eventData.put("server_id", serverId);
            eventData.put("event_type", eventType);
            eventData.put("player_name", player.getName());
            eventData.put("player_uuid", player.getUniqueId().toString());
            eventData.put("timestamp", System.currentTimeMillis());
            
            String jsonData = gson.toJson(eventData);
            
            URL url = new URL(PROXY_API_URL + "/events");
            HttpURLConnection conn = (HttpURLConnection) url.openConnection();
            conn.setRequestMethod("POST");
            conn.setRequestProperty("Content-Type", "application/json");
            conn.setDoOutput(true);
            
            try (OutputStream os = conn.getOutputStream()) {
                byte[] input = jsonData.getBytes("utf-8");
                os.write(input, 0, input.length);
            }
            
        } catch (Exception e) {
            getLogger().log(Level.WARNING, "Failed to send player event: " + e.getMessage());
        }
    }

    @Override
    public boolean onCommand(CommandSender sender, Command command, String label, String[] args) {
        if (!command.getName().equalsIgnoreCase("nautical")) {
            return false;
        }

        if (args.length == 0) {
            sender.sendMessage("§6Nautical Proxy Plugin v" + getDescription().getVersion());
            sender.sendMessage("§7Server ID: §f" + serverId);
            sender.sendMessage("§7Proxy: §f" + proxyHost + ":" + proxyPort);
            sender.sendMessage("§7Commands: §f/nautical [reload|status|test]");
            return true;
        }

        switch (args[0].toLowerCase()) {
            case "reload":
                if (!sender.hasPermission("nautical.admin")) {
                    sender.sendMessage("§cYou don't have permission to use this command.");
                    return true;
                }
                
                reloadConfig();
                loadConfiguration();
                sender.sendMessage("§aConfiguration reloaded!");
                return true;

            case "status":
                sender.sendMessage("§6Proxy Connection Status:");
                sender.sendMessage("§7Server ID: §f" + serverId);
                sender.sendMessage("§7Server Name: §f" + serverName);
                sender.sendMessage("§7Proxy Host: §f" + proxyHost + ":" + proxyPort);
                sender.sendMessage("§7Heartbeat: §a" + (heartbeatTask != null ? "Active" : "Inactive"));
                return true;

            case "test":
                if (!sender.hasPermission("nautical.admin")) {
                    sender.sendMessage("§cYou don't have permission to use this command.");
                    return true;
                }
                
                sender.sendMessage("§7Testing proxy connection...");
                getServer().getScheduler().runTaskAsynchronously(this, () -> {
                    try {
                        testProxyConnection();
                        sender.sendMessage("§aProxy connection test successful!");
                    } catch (Exception e) {
                        sender.sendMessage("§cProxy connection test failed: " + e.getMessage());
                    }
                });
                return true;

            default:
                sender.sendMessage("§cUnknown command. Use /nautical for help.");
                return true;
        }
    }

    private void testProxyConnection() throws Exception {
        URL url = new URL(PROXY_API_URL + "/health");
        HttpURLConnection conn = (HttpURLConnection) url.openConnection();
        conn.setRequestMethod("GET");
        conn.setConnectTimeout(5000);
        conn.setReadTimeout(5000);
        
        int responseCode = conn.getResponseCode();
        if (responseCode != 200) {
            throw new Exception("Proxy returned status code: " + responseCode);
        }
        
        // Read response
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(conn.getInputStream()))) {
            StringBuilder response = new StringBuilder();
            String line;
            while ((line = reader.readLine()) != null) {
                response.append(line);
            }
            
            // Parse JSON response to verify it's valid
            gson.fromJson(response.toString(), Map.class);
        }
    }

    // Inner class for server information
    public static class ServerInfo {
        public String id;
        public String name;
        public String address;
        public int port;
        public String status;
        public int player_count;
        public int max_players;
        public String motd;
        public String version;
        public long last_ping;
        public int priority;
        public Map<String, String> metadata;
    }
}

// plugin.yml content (create as separate file)
/*
name: NauticalProxy
version: 1.0.0
description: Nautical Proxy Plugin for Minecraft servers
author: NauticalProxy
main: com.nauticalproxy.plugin.NauticalProxyPlugin
api-version: 1.19
softdepend: []

commands:
  nautical:
    description: Nautical Proxy commands
    usage: /nautical [reload|status|test]
    permission: nautical.use
    permission-message: You don't have permission to use this command.

permissions:
  nautical.use:
    description: Allows use of basic nautical commands
    default: true
  nautical.admin:
    description: Allows use of admin nautical commands
    default: op
*/

// config.yml content (create as separate file)
/*
# Nautical Proxy Plugin Configuration

server:
  # Unique identifier for this server (auto-generated if not set)
  id: ""
  
  # Display name for this server
  name: "Minecraft Server"
  
  # Server address (IP/hostname) that the proxy should connect to
  address: "localhost"
  
  # Priority for load balancing (higher = more preferred)
  priority: 1

proxy:
  # Proxy API host
  host: "localhost"
  
  # Proxy API port
  port: 8080
  
  # API key for authentication (optional)
  api_key: ""

# Additional server metadata
metadata:
  server_type: "survival"
  description: "A Minecraft server"
*/

// pom.xml for Maven build (create as separate file)
/*
<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 
         http://maven.apache.org/xsd/maven-4.0.0.xsd">
    <modelVersion>4.0.0</modelVersion>
    
    <groupId>com.nauticalproxy</groupId>
    <artifactId>nautical-proxy-plugin</artifactId>
    <version>1.0.0</version>
    <packaging>jar</packaging>
    
    <properties>
        <maven.compiler.source>8</maven.compiler.source>
        <maven.compiler.target>8</maven.compiler.target>
        <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
    </properties>
    
    <repositories>
        <repository>
            <id>spigot-repo</id>
            <url>https://hub.spigotmc.org/nexus/content/repositories/snapshots/</url>
        </repository>
    </repositories>
    
    <dependencies>
        <dependency>
            <groupId>org.spigotmc</groupId>
            <artifactId>spigot-api</artifactId>
            <version>1.19.2-R0.1-SNAPSHOT</version>
            <scope>provided</scope>
        </dependency>
        <dependency>
            <groupId>com.google.code.gson</groupId>
            <artifactId>gson</artifactId>
            <version>2.8.9</version>
        </dependency>
    </dependencies>
    
    <build>
        <plugins>
            <plugin>
                <groupId>org.apache.maven.plugins</groupId>
                <artifactId>maven-compiler-plugin</artifactId>
                <version>3.8.1</version>
                <configuration>
                    <source>8</source>
                    <target>8</target>
                </configuration>
            </plugin>
            <plugin>
                <groupId>org.apache.maven.plugins</groupId>
                <artifactId>maven-shade-plugin</artifactId>
                <version>3.2.4</version>
                <executions>
                    <execution>
                        <phase>package</phase>
                        <goals>
                            <goal>shade</goal>
                        </goals>
                        <configuration>
                            <relocations>
                                <relocation>
                                    <pattern>com.google.gson</pattern>
                                    <shadedPattern>com.nauticalproxy.plugin.gson</shadedPattern>
                                </relocation>
                            </relocations>
                        </configuration>
                    </execution>
                </executions>
            </plugin>
        </plugins>
    </build>
</project>
*/