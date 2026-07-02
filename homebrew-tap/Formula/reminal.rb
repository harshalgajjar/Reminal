class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.0.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.0/reminal_1.0.0_darwin_arm64.tar.gz"
      sha256 "d441de58659d2d8085b6290fec15e6f0233cd0e5ee2438a557a2ea289ff3df59"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.0/reminal_1.0.0_darwin_amd64.tar.gz"
      sha256 "6f9784ed146889fada8ea02fa276ae083db781a6f6177a5c683e2139b10c379f"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.0/reminal_1.0.0_linux_arm64.tar.gz"
      sha256 "1f2de935775fac3adadb55c4bb24a8e212de37b2d82d6b840e9f8735bb612921"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.0/reminal_1.0.0_linux_amd64.tar.gz"
      sha256 "f2f737a7fff1228173181d0a6b74a6e677b10d8199d7c69598700468d8b83643"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
