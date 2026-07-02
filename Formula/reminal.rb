class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.11.2"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.11.2/reminal_0.11.2_darwin_arm64.tar.gz"
      sha256 "4fd1a56ade25eb1e9e8a38254231f755f2568482d5c1b6eb737fc18ef73f102c"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.11.2/reminal_0.11.2_darwin_amd64.tar.gz"
      sha256 "0a0f95abaeddadf4700d1c5d87876e9b86807a511259a54a09e4f355a10fc1de"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.11.2/reminal_0.11.2_linux_arm64.tar.gz"
      sha256 "73948040cfdef22c43837c843a6194a94030a4b3697306c3d897e92bbf1ed366"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.11.2/reminal_0.11.2_linux_amd64.tar.gz"
      sha256 "f6b1b704a39f14dbfb406a3ae3de70ae96f465cf52e93ecf2ce3425e23d2cbad"
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
