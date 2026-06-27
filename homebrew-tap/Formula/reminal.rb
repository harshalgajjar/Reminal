class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.9.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.2/reminal_0.9.2_darwin_arm64.tar.gz"
      sha256 "c3e20a659737cf16ec1f1dc653ed75213516be4f8eff686d8d6ba0af4de67e8b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.2/reminal_0.9.2_darwin_amd64.tar.gz"
      sha256 "625f548a3078024113f05f837499bc40be698aeda40a949901da47d3914ba92a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.2/reminal_0.9.2_linux_arm64.tar.gz"
      sha256 "8132eb212393d2871fabf5e81aea8aa9814d5d950cc3fef60987d50ec7080416"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.2/reminal_0.9.2_linux_amd64.tar.gz"
      sha256 "ffe7fb8825456d7fa6c362cd7a8f2a4f94c99ad3b40d31370b254b7eb34ddf4d"
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
